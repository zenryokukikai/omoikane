package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

func TestTierAPI(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodGet, base+"/v1/tiers?tier=3", tok, nil, nil)
	if s != 200 {
		t.Fatalf("tiers: %d", s)
	}
	// Default tier when omitted
	s, _ = doJSON(t, http.MethodGet, base+"/v1/tiers", tok, nil, nil)
	if s != 200 {
		t.Fatalf("default-tier: %d", s)
	}
	// Bad tier → 400
	s, _ = doJSON(t, http.MethodGet, base+"/v1/tiers?tier=99", tok, nil, nil)
	if s != 400 {
		t.Fatalf("bad-tier: %d", s)
	}
}

func TestCoordinatorTriageAPI(t *testing.T) {
	base, tok, _ := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)
	s, raw := doJSON(t, http.MethodGet,
		base+"/v1/librarian/coordinator/triage?missing_heartbeat_minutes=15", tok, nil, nil)
	if s != 200 {
		t.Fatalf("triage: %d %s", s, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if _, ok := out["ReviewQueueDepth"]; !ok {
		t.Fatalf("missing field: %s", raw)
	}
}

func TestCoordinatorProposeQuartetAPI(t *testing.T) {
	base, tok, _ := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)
	s, raw := doJSON(t, http.MethodPost,
		base+"/v1/librarian/coordinator/propose_quartet", tok,
		map[string]any{"topic": "test"}, nil)
	if s != 201 {
		t.Fatalf("propose: %d %s", s, raw)
	}
	s, _ = doJSON(t, http.MethodPost,
		base+"/v1/librarian/coordinator/propose_quartet", tok,
		map[string]any{}, nil)
	if s != 400 {
		t.Fatalf("missing-topic: %d", s)
	}
	if got := postRaw(t, http.MethodPost,
		base+"/v1/librarian/coordinator/propose_quartet", tok, "{"); got != 400 {
		t.Fatalf("bad-json: %d", got)
	}
}

func TestAdminBackup(t *testing.T) {
	base, tok, _ := testServer(t)
	target := filepath.Join(t.TempDir(), "backup.db")
	s, raw := doJSON(t, http.MethodPost, base+"/v1/admin/backup", tok,
		map[string]any{"path": target}, nil)
	if s != 201 {
		t.Fatalf("backup: %d %s", s, raw)
	}

	s, _ = doJSON(t, http.MethodPost, base+"/v1/admin/backup", tok,
		map[string]any{}, nil)
	if s != 400 {
		t.Fatalf("missing-path: %d", s)
	}
	if got := postRaw(t, http.MethodPost, base+"/v1/admin/backup", tok, "{"); got != 400 {
		t.Fatalf("bad-json: %d", got)
	}

	s, _ = doJSON(t, http.MethodGet, base+"/v1/admin/backups", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list: %d", s)
	}
}

func TestAdminDeadPoolAndUsage(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/admin/dead_pool/run", tok, nil, nil)
	if s != 200 {
		t.Fatalf("dead-pool: %d", s)
	}
	s, _ = doJSON(t, http.MethodGet, base+"/v1/admin/health/llm_usage?days=7", tok, nil, nil)
	if s != 200 {
		t.Fatalf("llm-usage: %d", s)
	}
	s, _ = doJSON(t, http.MethodGet, base+"/v1/admin/health/coverage", tok, nil, nil)
	if s != 200 {
		t.Fatalf("coverage: %d", s)
	}
}
