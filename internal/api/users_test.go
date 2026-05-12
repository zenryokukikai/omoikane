package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kojira/omoikane/internal/store"
)

// Seed: admin (bootstrapped by testServer) + alice (human) + alice's
// agent claude-helper. Used by the table-driven cases below.
func seedDirectory(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateUser(ctx, &store.User{
		ID: "alice", Name: "Alice", Role: "member", Email: "alice@x.com",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, &store.User{
		ID:           "claude-helper",
		Name:         "claude-helper",
		Role:         "agent",
		ParentUserID: "alice",
		Description:  "Alice's research assistant for the audit project",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGetUserPublicProfile(t *testing.T) {
	base, tok, st := testServer(t)
	seedDirectory(t, st)

	s, raw := doJSON(t, http.MethodGet, base+"/v1/users/claude-helper", tok, nil, nil)
	if s != 200 {
		t.Fatalf("status %d: %s", s, raw)
	}
	var p PublicProfile
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode: %v: %s", err, raw)
	}
	if p.ID != "claude-helper" || p.Name != "claude-helper" || p.Role != "agent" {
		t.Errorf("identity fields wrong: %+v", p)
	}
	if !strings.Contains(p.Description, "research assistant") {
		t.Errorf("description not surfaced: %q", p.Description)
	}
	if p.ParentUserID != "alice" || p.ParentName != "Alice" {
		t.Errorf("parent linkage wrong: %s / %s", p.ParentUserID, p.ParentName)
	}
}

// CRITICAL: email must never appear in the response — that's the
// privacy contract this endpoint commits to.
func TestGetUserOmitsEmail(t *testing.T) {
	base, tok, st := testServer(t)
	seedDirectory(t, st)

	s, raw := doJSON(t, http.MethodGet, base+"/v1/users/alice", tok, nil, nil)
	if s != 200 {
		t.Fatalf("status %d: %s", s, raw)
	}
	if strings.Contains(string(raw), "alice@x.com") {
		t.Fatalf("EMAIL LEAKED in public profile: %s", raw)
	}
	if strings.Contains(string(raw), `"email"`) {
		t.Fatalf("email field key leaked: %s", raw)
	}
}

func TestGetUserUnknownIsNotFound(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodGet, base+"/v1/users/no-such-user", tok, nil, nil)
	if s != http.StatusNotFound {
		t.Fatalf("want 404, got %d", s)
	}
}

func TestGetUserRequiresAuth(t *testing.T) {
	base, _, _ := testServer(t)
	s, _ := doJSON(t, http.MethodGet, base+"/v1/users/admin", "", nil, nil)
	// MISSING_TOKEN → 401
	if s != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", s)
	}
}

func TestListUsersFilterByAgent(t *testing.T) {
	base, tok, st := testServer(t)
	seedDirectory(t, st)

	s, raw := doJSON(t, http.MethodGet, base+"/v1/users?role=agent", tok, nil, nil)
	if s != 200 {
		t.Fatalf("status %d: %s", s, raw)
	}
	var out struct {
		Users []PublicProfile `json:"users"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, raw)
	}
	if len(out.Users) != 1 {
		t.Fatalf("want 1 agent, got %d: %+v", len(out.Users), out.Users)
	}
	if out.Users[0].Name != "claude-helper" {
		t.Errorf("unexpected agent: %s", out.Users[0].Name)
	}
	// Parent name should be filled even though Alice isn't an agent and
	// wasn't returned in this same query — the handler does the lookup.
	if out.Users[0].ParentName != "Alice" {
		t.Errorf("parent name lookup failed: %s", out.Users[0].ParentName)
	}
}

func TestListUsersUnfilteredReturnsAll(t *testing.T) {
	base, tok, st := testServer(t)
	seedDirectory(t, st)
	s, raw := doJSON(t, http.MethodGet, base+"/v1/users", tok, nil, nil)
	if s != 200 {
		t.Fatalf("status %d: %s", s, raw)
	}
	var out struct {
		Users []PublicProfile `json:"users"`
	}
	_ = json.Unmarshal(raw, &out)
	// admin + alice + claude-helper = 3
	if len(out.Users) != 3 {
		t.Fatalf("want 3, got %d: %+v", len(out.Users), out.Users)
	}
	// Sanity: no email anywhere in the payload.
	if strings.Contains(string(raw), "alice@x.com") {
		t.Fatal("email leaked in list response")
	}
}

func TestListUsersInvalidRoleRejected(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodGet, base+"/v1/users?role=cyborg", tok, nil, nil)
	if s != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", s)
	}
}

func TestListUsersInvalidLimitRejected(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodGet, base+"/v1/users?limit=abc", tok, nil, nil)
	if s != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", s)
	}
	s, _ = doJSON(t, http.MethodGet, base+"/v1/users?limit=0", tok, nil, nil)
	if s != http.StatusBadRequest {
		t.Fatalf("want 400 for limit=0, got %d", s)
	}
}
