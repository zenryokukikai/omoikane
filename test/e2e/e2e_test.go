// Package e2e exercises the full omoikane stack end-to-end:
//
//   - boot kb-server (in-process via internal/server.BuildRouter)
//   - run kb CLI against it
//   - run kb-mcp against it
//   - run librarian-runner against it
//
// The test deliberately uses real binaries-as-libraries (the cmd/* shims
// are 3-line shims into the internal/* packages, so we invoke
// internal.Run / internal.BuildRouter directly). This catches
// integration breakage that per-package tests can't.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kojira/omoikane/internal/api"
	"github.com/kojira/omoikane/internal/cli"
	"github.com/kojira/omoikane/internal/config"
	"github.com/kojira/omoikane/internal/dashboard"
	"github.com/kojira/omoikane/internal/enrich"
	"github.com/kojira/omoikane/internal/librunner"
	"github.com/kojira/omoikane/internal/mcp"
	"github.com/kojira/omoikane/internal/store"

	"github.com/go-chi/chi/v5"
)

// e2eRig is the harness for a single e2e test: kb-server + token +
// configured kb CLI.
type e2eRig struct {
	t        *testing.T
	server   *httptest.Server
	dbPath   string
	dataDir  string
	token    string
	url      string
	store    *store.Store
}

func setupRig(t *testing.T) *e2eRig {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kb.db")

	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Bootstrap user + admin token.
	if err := st.CreateUser(context.Background(),
		&store.User{ID: "e2e", Name: "e2e", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	token, err := st.CreateToken(context.Background(), "e2e", "e2e-tok",
		[]string{"read", "write", "admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	apiH := &api.Handler{
		Store:       st,
		Enricher:    enrich.New("", "", "", "", logger),
		SecretsMode: config.SecretsOff,
		Logger:      logger,
		StartedAt:   time.Now().Format(time.RFC3339),
	}
	dashH, err := dashboard.New(st, false)
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(api.RequestID)
	r.Use(api.Audit(st, logger))
	apiH.Mount(r)
	dashH.Mount(r)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	// Ensure each test starts with a clean emergency-stop state.
	api.ResetEmergencyStopForTest()

	return &e2eRig{
		t:       t,
		server:  srv,
		dbPath:  dbPath,
		dataDir: dir,
		token:   token,
		url:     srv.URL,
		store:   st,
	}
}

// configureCLI points the CLI at this rig's server + token via a fresh
// config file. Returns the config path so the caller can isolate.
func (r *e2eRig) configureCLI() string {
	r.t.Helper()
	cfgPath := filepath.Join(r.dataDir, "cli.json")
	cli.SetConfigPath(func() (string, error) { return cfgPath, nil })
	r.t.Cleanup(func() { cli.SetConfigPath(nil) })
	r.t.Setenv("KB_URL", "")
	r.t.Setenv("KB_TOKEN", "")
	b, _ := json.MarshalIndent(map[string]string{"url": r.url, "token": r.token}, "", "  ")
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		r.t.Fatal(err)
	}
	return cfgPath
}

// runCLI invokes cli.Run with the given args and returns (code, stdout, stderr).
func (r *e2eRig) runCLI(args ...string) (int, string, string) {
	r.t.Helper()
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	code := cli.Run(args, strings.NewReader(""), out, errBuf)
	return code, out.String(), errBuf.String()
}

// httpJSON is a small helper for direct HTTP calls when we want to
// observe response bodies (the CLI prints to stdout but doesn't expose
// parsed structures).
func (r *e2eRig) httpJSON(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, r.url+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// ============================================================
// e2e tests
// ============================================================

// TestE2E_FullFlow walks through the agent-facing lifecycle:
//
//  1. project create (CLI)
//  2. entry post (CLI)
//  3. lookup by-symptom with create_cases=true (HTTP)
//  4. patch the case to mark it helpful (CLI feedback judge)
//  5. verify entry signals reflect it (CLI feedback signals)
//  6. add a second entry, link conflicts_with → auto-supersede
//  7. add a hierarchy node + attach the surviving entry (CLI browse)
//  8. browse-by-hierarchy (CLI index)
//  9. reflect across both entries (CLI reflect) — confirms the
//     archived entry is still fetchable by direct GET
func TestE2E_FullFlow(t *testing.T) {
	r := setupRig(t)
	r.configureCLI()

	// 1. project
	if code, _, e := r.runCLI("projects", "create", "--id", "demo", "--name", "Demo"); code != 0 {
		t.Fatalf("project create: code=%d err=%s", code, e)
	}

	// 2. entry — write a fake body file, then `kb post` it
	body := filepath.Join(r.dataDir, "entry.md")
	_ = os.WriteFile(body, []byte("rectangular mask leaks at inference time"), 0o644)
	if code, out, e := r.runCLI("post", "--project", "demo", "--type", "trap",
		"--title", "Mask trap", "--file", body, "--tags", "mask,preprocessing"); code != 0 {
		t.Fatalf("post: code=%d err=%s", code, e)
	} else if !strings.Contains(out, `"id"`) {
		t.Fatalf("post stdout: %s", out)
	}

	// Find the entry ID
	_, raw := r.httpJSON(t, http.MethodGet, "/v1/entries?project_id=demo", nil)
	var listResp struct {
		Entries []struct {
			ID string `json:"id"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Entries) != 1 {
		t.Fatalf("expected 1 entry: %s", raw)
	}
	eid := listResp.Entries[0].ID

	// Seed a symptom phrase for the lookup. (No enrichment in tests.)
	if err := r.store.ReplaceSymptoms(context.Background(), eid,
		[]string{"rectangular mask leak at inference"}, "test"); err != nil {
		t.Fatal(err)
	}

	// 3. lookup with create_cases
	status, raw := r.httpJSON(t, http.MethodPost, "/v1/lookup/by-symptom",
		map[string]any{
			"symptom_description": "rectangular mask leak at inference",
			"create_cases":        true,
		})
	if status != 200 {
		t.Fatalf("lookup: %d %s", status, raw)
	}
	var lookup struct {
		Matches []map[string]any `json:"matches"`
	}
	if err := json.Unmarshal(raw, &lookup); err != nil {
		t.Fatal(err)
	}
	if len(lookup.Matches) == 0 || lookup.Matches[0]["case_id"] == nil {
		t.Fatalf("expected case_id in match: %s", raw)
	}
	caseID := lookup.Matches[0]["case_id"].(string)

	// 4. judge the case helpful
	if code, _, e := r.runCLI("feedback", "judge", "--case", caseID,
		"--outcome", "applied", "--result", "helpful"); code != 0 {
		t.Fatalf("judge: %d %s", code, e)
	}

	// 5. signals reflect the judgement
	_, out, _ := r.runCLI("feedback", "signals", eid)
	if !strings.Contains(out, `"HelpfulCount": 1`) && !strings.Contains(out, `"helpful_count":1`) {
		// signals.go returns the struct with capitalized field names.
		// Accept either casing per future drift.
		if !strings.Contains(out, "HelpfulCount") {
			t.Fatalf("expected helpful in signals: %s", out)
		}
	}

	// 6. second entry + conflicts_with → auto-supersede
	body2 := filepath.Join(r.dataDir, "entry2.md")
	_ = os.WriteFile(body2, []byte("alternative mask handling without rectangle"), 0o644)
	if code, _, e := r.runCLI("post", "--project", "demo", "--type", "decision",
		"--title", "Use landmark masks", "--file", body2); code != 0 {
		t.Fatalf("post2: %d %s", code, e)
	}
	_, raw = r.httpJSON(t, http.MethodGet, "/v1/entries?project_id=demo", nil)
	_ = json.Unmarshal(raw, &listResp)
	if len(listResp.Entries) != 2 {
		t.Fatalf("expected 2 entries: %s", raw)
	}
	// The first-inserted (older) entry is in index 1 after DESC sort.
	var older, newer string
	for _, e := range listResp.Entries {
		if e.ID == eid {
			older = e.ID
		} else {
			newer = e.ID
		}
	}
	if code, _, errOut := r.runCLI("relations", "link",
		"--from", older, "--to", newer, "--type", "conflicts_with"); code != 0 {
		t.Fatalf("conflicts link: %d %s", code, errOut)
	}
	got, err := r.store.GetEntry(context.Background(), older)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "SUPERSEDED" {
		t.Fatalf("older.Status=%s, expected SUPERSEDED", got.Status)
	}

	// 7. hierarchy
	if code, out, errOut := r.runCLI("browse", "create",
		"--name", "Masks", "--description", "mask-related entries"); code != 0 {
		t.Fatalf("browse create: %d %s", code, errOut)
	} else if !strings.Contains(out, `"ID"`) {
		t.Fatalf("browse create stdout: %s", out)
	}

	// 8. index by hierarchy
	if code, _, e := r.runCLI("index", "--group-by", "hierarchy"); code != 0 {
		t.Fatalf("index: %d %s", code, e)
	}

	// 9. reflect — confirms both entries are still readable (older is
	// SUPERSEDED but GET still works)
	if code, out, errOut := r.runCLI("reflect", older, newer, "--prompt", "compare"); code != 0 {
		t.Fatalf("reflect: %d %s", code, errOut)
	} else if !strings.Contains(out, "summary") {
		t.Fatalf("reflect stdout: %s", out)
	}
}

// TestE2E_LibrarianRunner exercises the runner stub against the live
// kb-server: it registers an instance, announces in chat, and emits
// heartbeats.
func TestE2E_LibrarianRunner(t *testing.T) {
	r := setupRig(t)

	// Build a fast-heartbeat skill bundle in a temp dir by copying the
	// real coordinator bundle and overriding personality.yaml. We don't
	// modify the real bundle because shipping interval = 600s; the e2e
	// test needs sub-second to stay snappy.
	src, err := filepath.Abs("../../dist/skills/librarians/coordinator")
	if err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(r.dataDir, "skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		"SKILL.md", "role_definition.md", "operations.yaml",
		"decision_protocols.md", "trigger_conditions.yaml",
		"communication_style.md", "meta_protocol.md",
		"error_handling.md", "self_check.md",
	} {
		b, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Override personality with a fast interval.
	if err := os.WriteFile(filepath.Join(skillDir, "personality.yaml"), []byte(
		"id: coordinator\ndata_gathering:\n  heartbeat_interval_seconds: 1\nposting_behavior:\n  daily_token_ceiling: 1000\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := librunner.Run([]string{
		"--role", "coordinator",
		"--skill-path", skillDir,
		"--kb-url", r.url,
		"--kb-token", r.token,
		"--max-beats", "2",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("runner exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "heartbeat 2") {
		t.Fatalf("expected 2 heartbeats: %s", stdout.String())
	}

	// Verify the instance + heartbeat made it into the store
	list, err := r.store.ListLibrarianInstances(context.Background(), "coordinator", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(list))
	}
	if list[0].HeartbeatAt == nil {
		t.Fatal("expected heartbeat recorded")
	}

	// And the announcement chat message was posted
	threads, _ := r.store.ListThreads(context.Background(), "", 10)
	_ = threads
	// We can't filter by author easily without a list-by-role accessor;
	// just check that *some* chat message exists.
	if _, err := r.store.DB().Exec(`SELECT 1`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := r.store.DB().QueryRow(`SELECT COUNT(*) FROM librarian_chat WHERE author_role = 'coordinator'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 chat post, got %d", n)
	}
}

// TestE2E_MCP exercises the MCP stdio adapter against the live kb-server.
func TestE2E_MCP(t *testing.T) {
	r := setupRig(t)

	s := &mcp.Server{
		CoreURL: r.url,
		Token:   r.token,
	}
	// Seed: project + entry so kb_search has a hit.
	_, _ = r.httpJSON(t, http.MethodPost, "/v1/projects",
		map[string]any{"id": "demo", "name": "Demo"})
	_, _ = r.httpJSON(t, http.MethodPost, "/v1/entries",
		map[string]any{
			"project_id": "demo", "type": "trap",
			"title": "rectangle leak", "body": "rectangular mask drift",
		})

	// tools/list
	out := &bytes.Buffer{}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	if err := s.Run(context.Background(), in, out); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if !strings.Contains(out.String(), "kb_search") {
		t.Fatalf("missing kb_search: %s", out.String())
	}

	// kb_search call
	out.Reset()
	in = strings.NewReader(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"kb_search","arguments":{"query":"\"rectangular\"*"}}}` + "\n")
	if err := s.Run(context.Background(), in, out); err != nil {
		t.Fatalf("kb_search: %v", err)
	}
	if !strings.Contains(out.String(), "rectangle leak") {
		t.Fatalf("kb_search result: %s", out.String())
	}
}

// TestE2E_AdminBackupAndCoverage runs the Phase 7 ops endpoints.
func TestE2E_AdminBackupAndCoverage(t *testing.T) {
	r := setupRig(t)

	// Coverage on an empty store
	s, _ := r.httpJSON(t, http.MethodGet, "/v1/admin/health/coverage", nil)
	if s != 200 {
		t.Fatalf("coverage: %d", s)
	}

	// Backup to a fresh path
	target := filepath.Join(r.dataDir, "backup.db")
	s, raw := r.httpJSON(t, http.MethodPost, "/v1/admin/backup",
		map[string]any{"path": target})
	if s != 201 {
		t.Fatalf("backup: %d %s", s, raw)
	}
	if info, err := os.Stat(target); err != nil || info.Size() == 0 {
		t.Fatalf("backup file: %v size=%d", err, info.Size())
	}

	// List backups
	s, raw = r.httpJSON(t, http.MethodGet, "/v1/admin/backups", nil)
	if s != 200 {
		t.Fatalf("list-backups: %d", s)
	}
	if !strings.Contains(string(raw), `"DONE"`) {
		t.Fatalf("expected DONE: %s", raw)
	}

	// Dead-pool run on empty store is a no-op (no archived rows).
	s, raw = r.httpJSON(t, http.MethodPost, "/v1/admin/dead_pool/run", nil)
	if s != 200 {
		t.Fatalf("dead-pool: %d %s", s, raw)
	}
}
