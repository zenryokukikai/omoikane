// Package librunner is the kid harness that loads a librarian skill
// bundle, registers an instance with the Core kb-server, and emits
// heartbeats. The actual LLM/tool loop is delegated to the configured
// agent runtime — this package is a thin Phase 5 stub that proves the
// integration contract is testable end-to-end before the real agent
// invocation is wired up.
package librunner

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Version is overridden at link time.
var Version = "0.7.0-phase5-stub"

// httpClientFn is overridable in tests so the harness can be exercised
// against an httptest.Server.
var httpClientFn = func() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// nowFn is overridable in tests.
var nowFn = time.Now

// SkillBundle is the deserialised form of a librarian/<role>/ directory.
// Only the fields the runner actually uses are extracted; the rest of
// the skill files remain on disk for the agent runtime to consume
// directly.
type SkillBundle struct {
	Role              string
	SkillPath         string
	HeartbeatInterval time.Duration
	CooldownInterval  time.Duration
	DailyTokenCeiling int
	Phase             int
	Prohibitions      []string
}

// skillFrontmatter is the subset of SKILL.md frontmatter the runner
// parses. The agent runtime (Claude Code / pi-agent / etc.) consumes the
// rest of the file directly; the runner only needs the operational
// params to schedule heartbeats and enforce budget ceilings.
type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Operational struct {
		HeartbeatIntervalSeconds      int `yaml:"heartbeat_interval_seconds"`
		CooldownBetweenActionsSeconds int `yaml:"cooldown_between_actions_seconds"`
		DailyTokenCeiling             int `yaml:"daily_token_ceiling"`
		Phase                         int `yaml:"phase"`
	} `yaml:"operational"`
	Prohibitions []string `yaml:"prohibitions"`
}

// parseFrontmatter extracts the YAML block delimited by the leading `---`
// and the next `---` of a markdown file. Returns ErrInvalidBundle if the
// file does not begin with `---`.
func parseFrontmatter(path string) (*skillFrontmatter, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Normalise line endings minimally.
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	// Expect first line to be exactly "---".
	const marker = "---\n"
	if !strings.HasPrefix(text, marker) {
		return nil, fmt.Errorf("frontmatter missing: %s does not start with '---'", path)
	}
	body := text[len(marker):]
	end := strings.Index(body, "\n---")
	if end == -1 {
		return nil, fmt.Errorf("frontmatter not terminated in %s", path)
	}
	yamlBlock := body[:end]
	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return nil, fmt.Errorf("frontmatter parse: %w", err)
	}
	return &fm, nil
}

// roleFromName extracts the bare role id from a frontmatter `name` of
// the form "omoikane-<role>". Falls back to the raw name if no prefix.
func roleFromName(name string) string {
	const prefix = "omoikane-"
	if strings.HasPrefix(name, prefix) {
		return name[len(prefix):]
	}
	return name
}

// LoadSkill reads a skill directory. Returns an error when any required
// file is missing or malformed — we want noisy failures rather than a
// silently degraded runner.
//
// New layout (migration 016 era): only 3 files are required —
//
//	SKILL.md (with frontmatter that carries operational params)
//	AGENTS.md
//	PERSONALITY.md
//
// The legacy 10-file layout (role_definition.md + personality.yaml + …)
// is no longer accepted; per-role bundles have been migrated.
func LoadSkill(dir string) (*SkillBundle, error) {
	required := []string{"SKILL.md", "AGENTS.md", "PERSONALITY.md"}
	for _, f := range required {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return nil, fmt.Errorf("missing %s: %w", f, err)
		}
	}
	fm, err := parseFrontmatter(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return nil, fmt.Errorf("SKILL.md: %w", err)
	}
	role := roleFromName(fm.Name)
	if role == "" {
		return nil, fmt.Errorf("SKILL.md: name is empty")
	}
	interval := time.Duration(fm.Operational.HeartbeatIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	cooldown := time.Duration(fm.Operational.CooldownBetweenActionsSeconds) * time.Second
	if cooldown <= 0 {
		cooldown = 60 * time.Second
	}
	phase := fm.Operational.Phase
	if phase == 0 {
		phase = 5
	}
	return &SkillBundle{
		Role:              role,
		SkillPath:         dir,
		HeartbeatInterval: interval,
		CooldownInterval:  cooldown,
		DailyTokenCeiling: fm.Operational.DailyTokenCeiling,
		Phase:             phase,
		Prohibitions:      fm.Prohibitions,
	}, nil
}

// Runner is the live runner state.
type Runner struct {
	CoreURL    string
	Token      string
	Skill      *SkillBundle
	InstanceID string
	Agent      string // 'stub' for Phase 5

	client *http.Client
}

// Run is the CLI entry. Returns a process exit code (0=clean,
// 1=runtime error, 2=usage).
func Run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("librarian-runner", flag.ContinueOnError)
	fs.SetOutput(stderr)
	role := fs.String("role", "", "librarian role (required)")
	skillPath := fs.String("skill-path", "", "path to skill bundle directory (required)")
	instanceID := fs.String("instance-id", "", "instance ID (optional; server mints if empty)")
	agent := fs.String("agent", "stub", "agent runtime: claude-code|opencode|stub")
	coreURL := fs.String("kb-url", envDefault("KB_CORE_URL", "http://localhost:8080"), "kb-server URL")
	token := fs.String("kb-token", os.Getenv("KB_TOKEN"), "kb-server bearer token")
	once := fs.Bool("once", false, "register + one heartbeat + exit (for tests)")
	maxBeats := fs.Int("max-beats", 0, "exit after N heartbeats (0 = forever)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *role == "" || *skillPath == "" {
		fmt.Fprintln(stderr, "--role and --skill-path are required")
		return 2
	}
	if *token == "" {
		fmt.Fprintln(stderr, "KB_TOKEN env var or --kb-token flag required")
		return 2
	}

	skill, err := LoadSkill(*skillPath)
	if err != nil {
		fmt.Fprintln(stderr, "skill load:", err)
		return 1
	}
	if skill.Role != *role {
		fmt.Fprintf(stderr, "skill role (%s) does not match --role (%s)\n", skill.Role, *role)
		return 1
	}

	r := &Runner{
		CoreURL:    strings.TrimRight(*coreURL, "/"),
		Token:      *token,
		Skill:      skill,
		InstanceID: *instanceID,
		Agent:      *agent,
		client:     httpClientFn(),
	}
	if err := r.Register(); err != nil {
		fmt.Fprintln(stderr, "register:", err)
		return 1
	}
	fmt.Fprintf(stdout, "registered %s as %s (agent=%s)\n", r.InstanceID, r.Skill.Role, r.Agent)

	if err := r.AnnounceObserving(); err != nil {
		// Non-fatal: don't bail if announcement chat post fails.
		fmt.Fprintln(stderr, "announce warn:", err)
	}

	beats := 0
	for {
		if err := r.Heartbeat(); err != nil {
			fmt.Fprintln(stderr, "heartbeat:", err)
			return 1
		}
		beats++
		fmt.Fprintf(stdout, "heartbeat %d\n", beats)
		if *once || (*maxBeats > 0 && beats >= *maxBeats) {
			return 0
		}
		time.Sleep(r.Skill.HeartbeatInterval)
	}
}

// Register POSTs /v1/librarian/instances. On success, populates
// r.InstanceID with the server-minted ID (if not provided by the caller).
func (r *Runner) Register() error {
	body := map[string]any{
		"role":          r.Skill.Role,
		"agent_runtime": r.Agent,
		"status":        "OBSERVING",
		"skill_version": Version,
	}
	if r.InstanceID != "" {
		body["instance_id"] = r.InstanceID
	}
	var out struct {
		InstanceID string `json:"instance_id"`
	}
	if err := r.do(http.MethodPost, "/v1/librarian/instances", body, &out); err != nil {
		return err
	}
	if out.InstanceID == "" {
		return errors.New("server returned empty instance_id")
	}
	r.InstanceID = out.InstanceID
	return nil
}

// AnnounceObserving posts a single chat message announcing the role is
// online in observation mode. Best-effort.
func (r *Runner) AnnounceObserving() error {
	body := map[string]any{
		"author_role":        r.Skill.Role,
		"author_instance_id": r.InstanceID,
		"intent":             "observation",
		"content": fmt.Sprintf(
			"%s online in OBSERVING mode (Phase 5 stub, agent=%s, heartbeat=%s).",
			r.Skill.Role, r.Agent, r.Skill.HeartbeatInterval),
	}
	return r.do(http.MethodPost, "/v1/librarian/chat", body, nil)
}

// Heartbeat POSTs /v1/librarian/instances/{id}/heartbeat. The Core
// stamps `heartbeat_at`.
func (r *Runner) Heartbeat() error {
	return r.do(http.MethodPost,
		"/v1/librarian/instances/"+r.InstanceID+"/heartbeat", nil, nil)
}

func (r *Runner) do(method, path string, body any, into any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, r.CoreURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("X-Client-Type", "librarian-runner")
	req.Header.Set("X-Client-Version", Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if into != nil && len(raw) > 0 {
		return json.Unmarshal(raw, into)
	}
	_ = nowFn // referenced so the override hook isn't a dead symbol in
	// the absence of any other consumer; future tests will use it for
	// deterministic timestamping.
	return nil
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
