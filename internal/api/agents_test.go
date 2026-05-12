package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/config"
	"github.com/kojira/omoikane/internal/enrich"
	"github.com/kojira/omoikane/internal/store"
)

// testServerOpenRegister mirrors testServer but flips RegisterOpen=true
// so the legacy/open registration path is exercised.
func testServerOpenRegister(t *testing.T) (base, tok string, st *store.Store) {
	t.Helper()
	dir := t.TempDir()
	var err error
	st, err = store.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(context.Background(),
		&store.User{ID: "admin", Name: "admin", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	tok, _ = st.CreateToken(context.Background(), "admin", "test",
		[]string{"read", "write", "admin"}, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &Handler{
		Store: st, Enricher: enrich.New("", "", "", "", logger),
		SecretsMode:  config.SecretsOff, Logger: logger,
		RegisterOpen: true,
	}
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Audit(st, logger))
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = st.Close() })
	return srv.URL, tok, st
}

func TestAgentRegisterRequiresInviteByDefault(t *testing.T) {
	base, _, _ := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)
	s, raw := doJSON(t, http.MethodPost, base+"/v1/agents/register", "",
		map[string]any{"name": "claude-code-test"}, nil)
	if s != 403 {
		t.Fatalf("expected 403 without invite, got %d: %s", s, raw)
	}
}

func TestAgentRegisterOpenFlow(t *testing.T) {
	base, _, _ := testServerOpenRegister(t)
	t.Cleanup(ResetEmergencyStopForTest)

	// With RegisterOpen=true the legacy unauthenticated flow works.
	s, raw := doJSON(t, http.MethodPost, base+"/v1/agents/register", "",
		map[string]any{"name": "claude-code-test", "description": "smoke"}, nil)
	if s != 201 {
		t.Fatalf("register: %d %s", s, raw)
	}
	var out struct {
		AgentID   string `json:"agent_id"`
		APIKey    string `json:"api_key"`
		ClaimCode string `json:"claim_code"`
		ClaimURL  string `json:"claim_url"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.AgentID == "" || out.APIKey == "" || out.ClaimCode == "" {
		t.Fatalf("incomplete: %+v", out)
	}
	if !strings.Contains(out.ClaimURL, out.ClaimCode) {
		t.Fatalf("claim URL: %s", out.ClaimURL)
	}
	s, _ = doJSON(t, http.MethodGet, base+"/v1/auth/me", out.APIKey, nil, nil)
	if s != 200 {
		t.Fatalf("agent token auth: %d", s)
	}
}

func TestAgentRegisterWithInvite(t *testing.T) {
	base, tok, st := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)

	// Admin issues an invite
	s, raw := doJSON(t, http.MethodPost, base+"/v1/admin/agent-invites", tok,
		map[string]any{"note": "for the lipsync project"}, nil)
	if s != 201 {
		t.Fatalf("issue invite: %d %s", s, raw)
	}
	var inv struct {
		Code         string `json:"code"`
		RegisterURL  string `json:"register_url"`
		Instructions string `json:"instructions"`
	}
	_ = json.Unmarshal(raw, &inv)
	if inv.Code == "" {
		t.Fatalf("no code: %s", raw)
	}

	// Agent redeems it (no auth needed — the code is the auth)
	s, raw = doJSON(t, http.MethodPost, base+"/v1/agents/register", "",
		map[string]any{
			"name": "lipsync-agent", "description": "X",
			"invitation_code": inv.Code,
		}, nil)
	if s != 201 {
		t.Fatalf("redeem: %d %s", s, raw)
	}
	var out struct {
		AgentID   string `json:"agent_id"`
		APIKey    string `json:"api_key"`
		ParentID  string `json:"parent_user_id"`
		ClaimCode string `json:"claim_code"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.ParentID != "admin" {
		t.Fatalf("parent: %s", out.ParentID)
	}
	if out.ClaimCode != "" {
		t.Fatalf("invite-redeem should not return claim_code: %s", out.ClaimCode)
	}
	// Agent's user record has parent_user_id set
	u, _ := st.GetUser(context.Background(), out.AgentID)
	if u.ParentUserID != "admin" {
		t.Fatalf("user.parent: %s", u.ParentUserID)
	}
	// Token works
	s, _ = doJSON(t, http.MethodGet, base+"/v1/auth/me", out.APIKey, nil, nil)
	if s != 200 {
		t.Fatalf("agent token auth: %d", s)
	}

	// Invite is single-use — re-redeem fails
	s, _ = doJSON(t, http.MethodPost, base+"/v1/agents/register", "",
		map[string]any{"name": "agent-2", "invitation_code": inv.Code}, nil)
	if s != 409 {
		t.Fatalf("reuse should fail: %d", s)
	}
}

func TestIssueAgentInviteRequiresAuth(t *testing.T) {
	base, _, _ := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/admin/agent-invites", "", nil, nil)
	if s != 401 {
		t.Fatalf("expected 401, got %d", s)
	}
}

func TestListAgentInvites(t *testing.T) {
	base, tok, _ := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)
	// Issue two invites
	for _, note := range []string{"first", "second"} {
		doJSON(t, http.MethodPost, base+"/v1/admin/agent-invites", tok,
			map[string]any{"note": note}, nil)
	}
	s, raw := doJSON(t, http.MethodGet, base+"/v1/admin/agent-invites", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list: %d", s)
	}
	var out struct {
		Invitations []map[string]any `json:"invitations"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Invitations) != 2 {
		t.Fatalf("len: %d", len(out.Invitations))
	}
}

func TestAgentRegisterValidation(t *testing.T) {
	base, _, _ := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/agents/register", "", map[string]any{}, nil)
	if s != 400 {
		t.Fatalf("empty: %d", s)
	}
	if got := postRaw(t, http.MethodPost, base+"/v1/agents/register", "", "{"); got != 400 {
		t.Fatalf("bad-json: %d", got)
	}
}

func TestAgentClaimGetPublic(t *testing.T) {
	base, _, st := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)
	reg, _ := st.RegisterAgent(context.Background(), "test-agent", "")

	// Public — no auth needed to inspect
	s, raw := doJSON(t, http.MethodGet, base+"/v1/agents/claim/"+reg.ClaimCode, "", nil, nil)
	if s != 200 {
		t.Fatalf("get claim: %d %s", s, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	agent := out["agent"].(map[string]any)
	if agent["name"] != "test-agent" {
		t.Fatalf("agent: %+v", agent)
	}
}

func TestAgentClaimGetMissing(t *testing.T) {
	base, _, _ := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)
	s, _ := doJSON(t, http.MethodGet, base+"/v1/agents/claim/missing", "", nil, nil)
	if s != 404 {
		t.Fatalf("missing: %d", s)
	}
}

func TestAgentClaimPostHappyPath(t *testing.T) {
	base, tok, st := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)
	reg, _ := st.RegisterAgent(context.Background(), "test-agent", "")

	// Claim using the admin's bearer token
	s, _ := doJSON(t, http.MethodPost, base+"/v1/agents/claim/"+reg.ClaimCode, tok, nil, nil)
	if s != 204 {
		t.Fatalf("claim: %d", s)
	}
	// Agent now has parent_user_id = admin
	u, _ := st.GetUser(context.Background(), reg.AgentUser.ID)
	if u.ParentUserID != "admin" {
		t.Fatalf("parent: %s", u.ParentUserID)
	}
}

func TestAgentClaimPostRequiresAuth(t *testing.T) {
	base, _, st := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)
	reg, _ := st.RegisterAgent(context.Background(), "test-agent", "")
	s, _ := doJSON(t, http.MethodPost, base+"/v1/agents/claim/"+reg.ClaimCode, "", nil, nil)
	if s != 401 {
		t.Fatalf("expected 401, got %d", s)
	}
}

func TestAgentClaimPostUnknownCode(t *testing.T) {
	base, tok, _ := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/agents/claim/missing", tok, nil, nil)
	if s != 404 {
		t.Fatalf("missing code: %d", s)
	}
}

func TestAgentClaimPostStolenByOtherUser(t *testing.T) {
	base, _, st := testServer(t)
	t.Cleanup(ResetEmergencyStopForTest)
	ctx := context.Background()

	// Set up two distinct human users with their own tokens
	_ = st.CreateUser(ctx, &store.User{ID: "u-alice", Name: "Alice"})
	_ = st.CreateUser(ctx, &store.User{ID: "u-bob", Name: "Bob"})
	aliceTok, _ := st.CreateToken(ctx, "u-alice", "alice", []string{"read", "write"}, nil)
	bobTok, _ := st.CreateToken(ctx, "u-bob", "bob", []string{"read", "write"}, nil)

	reg, _ := st.RegisterAgent(ctx, "agent", "")
	// Alice claims first
	s, _ := doJSON(t, http.MethodPost, base+"/v1/agents/claim/"+reg.ClaimCode, aliceTok, nil, nil)
	if s != 204 {
		t.Fatalf("alice: %d", s)
	}
	// Bob tries to steal → 409
	s, _ = doJSON(t, http.MethodPost, base+"/v1/agents/claim/"+reg.ClaimCode, bobTok, nil, nil)
	if s != 409 {
		t.Fatalf("bob steal: %d", s)
	}
}
