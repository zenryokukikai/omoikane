package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kojira/omoikane/internal/store"
)

// Member invite: admin issues, captures code + claim URL.
func TestIssueMemberInvitation(t *testing.T) {
	base, tok, _ := testServer(t)
	s, raw := doJSON(t, http.MethodPost, base+"/v1/admin/members/invitations", tok,
		map[string]any{"email": "newperson@example.com", "role": "member", "note": "qa lead"}, nil)
	if s != http.StatusCreated {
		t.Fatalf("status %d: %s", s, raw)
	}
	var out struct {
		Code        string `json:"code"`
		TargetEmail string `json:"target_email"`
		TargetRole  string `json:"target_role"`
		ClaimURL    string `json:"claim_url"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Code == "" || out.TargetEmail != "newperson@example.com" || out.TargetRole != "member" {
		t.Fatalf("bad response: %+v", out)
	}
	if !strings.Contains(out.ClaimURL, out.Code) {
		t.Errorf("claim url missing code: %s", out.ClaimURL)
	}
}

// Email is required.
func TestIssueMemberInvitationRequiresEmail(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/admin/members/invitations", tok,
		map[string]any{"role": "member"}, nil)
	if s != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", s)
	}
}

// Role must be admin or member; any other role is rejected.
func TestIssueMemberInvitationRejectsInvalidRole(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/admin/members/invitations", tok,
		map[string]any{"email": "x@y.com", "role": "superadmin"}, nil)
	if s != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", s)
	}
}

// CRITICAL: non-admin cannot issue member invitations. The route is
// gated by RequireScope("admin"); a token without admin scope must be
// rejected with 403.
func TestIssueMemberInvitationRequiresAdmin(t *testing.T) {
	base, _, st := testServer(t)
	ctx := context.Background()
	_ = st.CreateUser(ctx, &store.User{ID: "alice", Name: "Alice", Role: "member"})
	memberTok, _ := st.CreateToken(ctx, "alice", "test",
		[]string{"read", "write"}, nil) // NO admin scope

	s, _ := doJSON(t, http.MethodPost, base+"/v1/admin/members/invitations", memberTok,
		map[string]any{"email": "spam@example.com", "role": "admin"}, nil)
	if s != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", s)
	}
}

// CRITICAL: admin can promote member → admin and demote admin → member.
func TestUpdateUserRolePromoteAndDemote(t *testing.T) {
	base, tok, st := testServer(t)
	ctx := context.Background()
	_ = st.CreateUser(ctx, &store.User{ID: "alice", Name: "Alice", Role: "member"})

	s, raw := doJSON(t, http.MethodPatch, base+"/v1/admin/users/alice/role", tok,
		map[string]any{"role": "admin"}, nil)
	if s != 200 {
		t.Fatalf("promote: %d %s", s, raw)
	}
	u, _ := st.GetUser(ctx, "alice")
	if u.Role != "admin" {
		t.Errorf("not promoted: %s", u.Role)
	}

	s, _ = doJSON(t, http.MethodPatch, base+"/v1/admin/users/alice/role", tok,
		map[string]any{"role": "member"}, nil)
	if s != 200 {
		t.Fatalf("demote: %d", s)
	}
	u, _ = st.GetUser(ctx, "alice")
	if u.Role != "member" {
		t.Errorf("not demoted: %s", u.Role)
	}
}

// CRITICAL: agent role cannot be changed via this endpoint. Promoting
// an agent to admin would break the parent_user_id invariant and
// blur the human/agent distinction the whole audit-log thesis rests
// on. Must be rejected at the store layer regardless of who calls.
func TestUpdateUserRoleRefusesAgent(t *testing.T) {
	base, tok, st := testServer(t)
	ctx := context.Background()
	_ = st.CreateUser(ctx, &store.User{
		ID: "agent-x", Name: "agent-x", Role: "agent", ParentUserID: "admin",
	})
	s, raw := doJSON(t, http.MethodPatch, base+"/v1/admin/users/agent-x/role", tok,
		map[string]any{"role": "admin"}, nil)
	if s == 200 {
		t.Fatalf("CRITICAL: agent was promoted to admin (audit thesis broken): %s", raw)
	}
	// Verify the role really wasn't touched.
	u, _ := st.GetUser(ctx, "agent-x")
	if u.Role != "agent" {
		t.Fatalf("agent role was modified: %s", u.Role)
	}
}

// CRITICAL: cannot demote the last admin — would lock everyone out
// of the admin surface.
func TestUpdateUserRoleRefusesLastAdminDemote(t *testing.T) {
	base, tok, _ := testServer(t)
	// 'admin' is the only admin in this fresh test setup.
	s, raw := doJSON(t, http.MethodPatch, base+"/v1/admin/users/admin/role", tok,
		map[string]any{"role": "member"}, nil)
	if s == 200 {
		t.Fatalf("last admin was demoted (lockout): %s", raw)
	}
}

// Last-admin lockout doesn't fire when there's a second admin.
func TestUpdateUserRoleAllowsDemoteWhenAnotherAdminExists(t *testing.T) {
	base, tok, st := testServer(t)
	ctx := context.Background()
	_ = st.CreateUser(ctx, &store.User{ID: "bob", Name: "Bob", Role: "admin"})

	s, raw := doJSON(t, http.MethodPatch, base+"/v1/admin/users/admin/role", tok,
		map[string]any{"role": "member"}, nil)
	if s != 200 {
		t.Fatalf("demote should succeed when another admin exists: %d %s", s, raw)
	}
}

// CRITICAL: non-admin cannot change anyone's role (gated by route).
func TestUpdateUserRoleRequiresAdmin(t *testing.T) {
	base, _, st := testServer(t)
	ctx := context.Background()
	_ = st.CreateUser(ctx, &store.User{ID: "alice", Name: "Alice", Role: "member"})
	memberTok, _ := st.CreateToken(ctx, "alice", "test",
		[]string{"read", "write"}, nil) // NO admin scope

	// alice tries to promote herself
	s, _ := doJSON(t, http.MethodPatch, base+"/v1/admin/users/alice/role", memberTok,
		map[string]any{"role": "admin"}, nil)
	if s != http.StatusForbidden {
		t.Fatalf("CRITICAL self-promotion: expected 403, got %d", s)
	}
}

func TestListMemberInvitations(t *testing.T) {
	base, tok, _ := testServer(t)
	// Issue two invites.
	_, _ = doJSON(t, http.MethodPost, base+"/v1/admin/members/invitations", tok,
		map[string]any{"email": "a@x.com"}, nil)
	_, _ = doJSON(t, http.MethodPost, base+"/v1/admin/members/invitations", tok,
		map[string]any{"email": "b@x.com", "role": "admin"}, nil)

	s, raw := doJSON(t, http.MethodGet, base+"/v1/admin/members/invitations", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list: %d %s", s, raw)
	}
	var out struct {
		Invitations []store.MemberInvitation `json:"invitations"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Invitations) != 2 {
		t.Fatalf("want 2, got %d", len(out.Invitations))
	}
}
