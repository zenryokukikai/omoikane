package librunner

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill builds a minimal skill directory in a temp dir and returns
// the path.
func writeSkill(t *testing.T, role string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"SKILL.md":               "skill",
		"role_definition.md":     "role",
		"personality.yaml":       "id: " + role + "\ndata_gathering:\n  heartbeat_interval_seconds: 1\nposting_behavior:\n  daily_token_ceiling: 1000\n",
		"operations.yaml":        "read: []\nwrite: []",
		"decision_protocols.md":  "decisions",
		"trigger_conditions.yaml": "heartbeat:\n  enabled: true\n",
		"communication_style.md": "tone",
		"meta_protocol.md":       "meta",
		"error_handling.md":      "err",
		"self_check.md":          "self",
	}
	for f, c := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadSkill(t *testing.T) {
	dir := writeSkill(t, "detective")
	skill, err := LoadSkill(dir)
	if err != nil {
		t.Fatal(err)
	}
	if skill.Role != "detective" {
		t.Fatalf("role=%s", skill.Role)
	}
	if skill.HeartbeatInterval.Seconds() != 1 {
		t.Fatalf("interval=%s", skill.HeartbeatInterval)
	}
}

func TestLoadSkillMissingFile(t *testing.T) {
	dir := writeSkill(t, "detective")
	_ = os.Remove(filepath.Join(dir, "self_check.md"))
	if _, err := LoadSkill(dir); err == nil {
		t.Fatal("expected error on missing file")
	}
}

func TestLoadSkillEmptyID(t *testing.T) {
	dir := writeSkill(t, "detective")
	_ = os.WriteFile(filepath.Join(dir, "personality.yaml"),
		[]byte("data_gathering:\n  heartbeat_interval_seconds: 1\n"), 0o644)
	if _, err := LoadSkill(dir); err == nil {
		t.Fatal("expected empty-id error")
	}
}

func TestLoadSkillBadYAML(t *testing.T) {
	dir := writeSkill(t, "detective")
	_ = os.WriteFile(filepath.Join(dir, "personality.yaml"), []byte("not: : valid: yaml: ["), 0o644)
	if _, err := LoadSkill(dir); err == nil {
		t.Fatal("expected yaml error")
	}
}

func TestLoadSkillDefaultInterval(t *testing.T) {
	dir := writeSkill(t, "detective")
	_ = os.WriteFile(filepath.Join(dir, "personality.yaml"),
		[]byte("id: detective\n"), 0o644)
	s, err := LoadSkill(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.HeartbeatInterval.Minutes() != 10 {
		t.Fatalf("expected 10m default, got %s", s.HeartbeatInterval)
	}
}

// stubCore returns an httptest.Server that responds to register +
// heartbeat + chat-post like the real kb-server would. It records the
// last request method/path for assertions.
func stubCore(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	hits := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits = append(*hits, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/v1/librarian/instances" && r.Method == "POST":
			_, _ = w.Write([]byte(`{"instance_id":"detective-stub"}`))
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			w.WriteHeader(204)
		case r.URL.Path == "/v1/librarian/chat":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"msg-1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

func TestRunHappyPath(t *testing.T) {
	dir := writeSkill(t, "detective")
	srv, hits := stubCore(t)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := Run([]string{
		"--role", "detective",
		"--skill-path", dir,
		"--kb-url", srv.URL,
		"--kb-token", "tok",
		"--once",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	// We expect: register, optional chat, heartbeat (the optional chat
	// is best-effort and might post before heartbeat).
	want := map[string]bool{
		"POST /v1/librarian/instances":                        true,
		"POST /v1/librarian/instances/detective-stub/heartbeat": true,
	}
	for _, h := range *hits {
		delete(want, h)
	}
	if len(want) > 0 {
		t.Fatalf("missing hits: %v (got %v)", want, *hits)
	}
}

func TestRunMaxBeats(t *testing.T) {
	dir := writeSkill(t, "detective")
	srv, hits := stubCore(t)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := Run([]string{
		"--role", "detective",
		"--skill-path", dir,
		"--kb-url", srv.URL,
		"--kb-token", "tok",
		"--max-beats", "2",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	beatCount := 0
	for _, h := range *hits {
		if strings.HasSuffix(h, "/heartbeat") {
			beatCount++
		}
	}
	if beatCount != 2 {
		t.Fatalf("expected 2 beats, got %d (%v)", beatCount, *hits)
	}
}

func TestRunMissingFlags(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := Run([]string{"--role", "detective"}, stdout, stderr); code != 2 {
		t.Fatalf("missing-skill: code=%d", code)
	}
	if code := Run([]string{"--role", "x", "--skill-path", "/tmp", "--kb-token", ""}, stdout, stderr); code != 2 {
		t.Fatalf("missing-token: code=%d", code)
	}
}

func TestRunMissingSkillDir(t *testing.T) {
	code := Run([]string{
		"--role", "detective",
		"--skill-path", "/tmp/nonexistent-xyz",
		"--kb-token", "tok",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("expected 1, got %d", code)
	}
}

func TestRunRoleMismatch(t *testing.T) {
	dir := writeSkill(t, "scout")
	code := Run([]string{
		"--role", "detective",
		"--skill-path", dir,
		"--kb-token", "tok",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("expected role mismatch: %d", code)
	}
}

func TestRunRegisterFailure(t *testing.T) {
	dir := writeSkill(t, "detective")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	code := Run([]string{
		"--role", "detective",
		"--skill-path", dir,
		"--kb-url", srv.URL,
		"--kb-token", "tok",
		"--once",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("expected failure: %d", code)
	}
}

func TestRunEmptyInstanceIDFromServer(t *testing.T) {
	dir := writeSkill(t, "detective")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`)) // empty instance_id
	}))
	defer srv.Close()
	code := Run([]string{
		"--role", "detective",
		"--skill-path", dir,
		"--kb-url", srv.URL,
		"--kb-token", "tok",
		"--once",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("expected error on empty instance_id: %d", code)
	}
}
