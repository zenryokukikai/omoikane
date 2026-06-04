package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kojira/omoikane/internal/store"
)

// End-to-end of the new librarian-role flow:
//   1. admin POSTs /v1/admin/agent-invites with librarian_role=cataloger
//   2. an agent POSTs /v1/agents/register with the returned code
//   3. the redeemed token's user has librarian_role set
//   4. that token successfully registers a librarian instance with
//      role=cataloger
//   5. that token CANNOT register a librarian instance with role=curator
func TestIssueAgentInviteWithLibrarianRoleE2E(t *testing.T) {
	base, adminTok, _ := testServer(t)

	// Step 1: admin issues invite with librarian_role
	s, raw := doJSON(t, http.MethodPost, base+"/v1/admin/agent-invites", adminTok,
		map[string]any{"note": "test cataloger seat", "librarian_role": "cataloger"}, nil)
	if s != 201 {
		t.Fatalf("issue invite: %d %s", s, raw)
	}
	var inv struct {
		Code          string `json:"code"`
		LibrarianRole string `json:"librarian_role"`
	}
	_ = json.Unmarshal(raw, &inv)
	if inv.LibrarianRole != "cataloger" {
		t.Fatalf("invite response missing librarian_role: %s", raw)
	}

	// Step 2: agent redeems invite
	s, raw = doJSON(t, http.MethodPost, base+"/v1/agents/register", "",
		map[string]any{
			"name":            "test-cataloger-agent",
			"invitation_code": inv.Code,
		}, nil)
	if s != 201 {
		t.Fatalf("register: %d %s", s, raw)
	}
	var reg struct {
		APIKey   string `json:"api_key"`
		AgentID  string `json:"agent_id"`
	}
	_ = json.Unmarshal(raw, &reg)
	agentTok := reg.APIKey
	if agentTok == "" {
		t.Fatal("missing api_key")
	}

	// Step 4: agent registers librarian instance as cataloger — SHOULD succeed
	s, raw = doJSON(t, http.MethodPost, base+"/v1/librarian/instances", agentTok,
		map[string]any{"role": "cataloger", "instance_label": "test"}, nil)
	if s != 201 {
		t.Fatalf("librarian register cataloger: %d %s", s, raw)
	}

	// Step 5: same agent tries to register as curator — SHOULD fail with 403
	s, raw = doJSON(t, http.MethodPost, base+"/v1/librarian/instances", agentTok,
		map[string]any{"role": "curator", "instance_label": "should-fail"}, nil)
	if s != http.StatusForbidden {
		t.Fatalf("expected 403 role-mismatch, got %d %s", s, raw)
	}
}

// An invite with an unknown librarian_role is rejected at issue time
// with the allowed vocabulary echoed back (so callers can self-correct).
func TestIssueAgentInviteUnknownRole(t *testing.T) {
	base, adminTok, _ := testServer(t)
	s, raw := doJSON(t, http.MethodPost, base+"/v1/admin/agent-invites", adminTok,
		map[string]any{"librarian_role": "wizard"}, nil)
	if s != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d %s", s, raw)
	}
	var resp struct {
		Error struct {
			Details struct {
				Allowed []string `json:"allowed"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &resp)
	if len(resp.Error.Details.Allowed) != 9 {
		t.Errorf("expected 9 allowed roles echoed, got %d: %s",
			len(resp.Error.Details.Allowed), raw)
	}
}

// An ordinary agent (no librarian_role on invite) gets read+write scope
// only — cannot register a librarian instance.
func TestOrdinaryAgentCannotRegisterAsLibrarian(t *testing.T) {
	base, adminTok, _ := testServer(t)

	// Issue ordinary invite (no librarian_role)
	s, raw := doJSON(t, http.MethodPost, base+"/v1/admin/agent-invites", adminTok,
		map[string]any{"note": "ordinary agent"}, nil)
	if s != 201 {
		t.Fatalf("issue invite: %d %s", s, raw)
	}
	var inv struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &inv)

	s, raw = doJSON(t, http.MethodPost, base+"/v1/agents/register", "",
		map[string]any{"name": "ordinary-agent", "invitation_code": inv.Code}, nil)
	if s != 201 {
		t.Fatalf("register: %d %s", s, raw)
	}
	var reg struct {
		APIKey string `json:"api_key"`
	}
	_ = json.Unmarshal(raw, &reg)

	// Try to register librarian instance — must fail with 403 (no scope).
	s, raw = doJSON(t, http.MethodPost, base+"/v1/librarian/instances", reg.APIKey,
		map[string]any{"role": "cataloger", "instance_label": "x"}, nil)
	if s != http.StatusForbidden {
		t.Fatalf("expected 403, got %d %s", s, raw)
	}
}

// Store-level: redeeming a librarian invite sets librarian_role on the
// user and grants the `librarian` scope on the token.
func TestRedeemLibrarianInviteScopesAndRole(t *testing.T) {
	base, adminTok, st := testServer(t)
	_ = base

	ctx := context.Background()
	// Use the store API directly to confirm scope assignment.
	inviter := "admin"
	inv, err := st.CreateAgentInvitation(ctx, inviter, "test", "curator")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := st.RedeemAgentInvitation(ctx, inv.Code, "curator-test", "")
	if err != nil {
		t.Fatal(err)
	}
	if reg.AgentUser.LibrarianRole != "curator" {
		t.Errorf("user librarian_role: %q", reg.AgentUser.LibrarianRole)
	}
	// Probe the token's scopes by looking it up.
	at, err := st.LookupToken(ctx, reg.APIToken)
	if err != nil {
		t.Fatal(err)
	}
	if !store.HasScope(at.Scopes, "librarian") {
		t.Errorf("token missing librarian scope: %v", at.Scopes)
	}
	// admin scope NOT granted to librarian tokens.
	if store.HasScope(at.Scopes, "admin") {
		t.Errorf("token should not have admin scope: %v", at.Scopes)
	}
	_ = adminTok
}
