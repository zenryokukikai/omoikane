package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// TestAdminSpacesCRUD drives the /v1/admin/spaces|groups management
// surface end to end: create group + space, membership, grant, revoke —
// verifying effects through the store (primary evidence), not just
// status codes.
func TestAdminSpacesCRUD(t *testing.T) {
	base, adminTok, st := testServer(t)
	ctx := context.Background()

	if err := st.CreateUser(ctx, &store.User{ID: "u-bob", Name: "Bob", Role: "member"}); err != nil {
		t.Fatal(err)
	}

	// Create a group.
	code, raw := doJSON(t, "POST", base+"/v1/admin/groups", adminTok,
		map[string]any{"name": "dev-team"}, nil)
	if code != http.StatusCreated {
		t.Fatalf("create group: %d %s", code, raw)
	}
	var g store.Group
	if err := json.Unmarshal(raw, &g); err != nil || g.ID == "" {
		t.Fatalf("bad group payload: %s", raw)
	}

	// Create a space.
	code, raw = doJSON(t, "POST", base+"/v1/admin/spaces", adminTok,
		map[string]any{"name": "project-x"}, nil)
	if code != http.StatusCreated {
		t.Fatalf("create space: %d %s", code, raw)
	}
	var sp store.Space
	if err := json.Unmarshal(raw, &sp); err != nil || sp.ID == "" {
		t.Fatalf("bad space payload: %s", raw)
	}
	if sp.Kind != store.SpaceKindRestricted {
		t.Fatalf("created space kind = %q, want restricted", sp.Kind)
	}

	// Add member; grant the group on the space.
	if code, raw = doJSON(t, "PUT", base+"/v1/admin/groups/"+g.ID+"/members/u-bob", adminTok, nil, nil); code != 204 {
		t.Fatalf("add member: %d %s", code, raw)
	}
	if code, raw = doJSON(t, "PUT", base+"/v1/admin/spaces/"+sp.ID+"/acl/"+g.ID, adminTok,
		map[string]any{"role": "member"}, nil); code != 204 {
		t.Fatalf("set acl: %d %s", code, raw)
	}

	// Primary evidence: bob's resolved visibility now includes the space.
	vis, err := st.VisibleSpaces(ctx, "u-bob")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range vis {
		if s == sp.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("grant did not reach VisibleSpaces: %v", vis)
	}

	// Listings paint the whole table.
	code, raw = doJSON(t, "GET", base+"/v1/admin/spaces", adminTok, nil, nil)
	if code != 200 {
		t.Fatalf("list spaces: %d", code)
	}
	var spl struct {
		Spaces []struct {
			store.Space
			ACL []*store.SpaceACL `json:"acl"`
		} `json:"spaces"`
	}
	if err := json.Unmarshal(raw, &spl); err != nil {
		t.Fatal(err)
	}
	var sawGrant, sawInternal bool
	for _, row := range spl.Spaces {
		if row.ID == store.SpaceInternal {
			sawInternal = true
		}
		if row.ID == sp.ID {
			for _, a := range row.ACL {
				if a.GroupID == g.ID && a.Role == "member" {
					sawGrant = true
				}
			}
		}
	}
	if !sawInternal || !sawGrant {
		t.Fatalf("spaces listing incomplete (internal=%v grant=%v): %s", sawInternal, sawGrant, raw)
	}

	code, raw = doJSON(t, "GET", base+"/v1/admin/groups", adminTok, nil, nil)
	if code != 200 {
		t.Fatalf("list groups: %d", code)
	}
	var gl struct {
		Groups []struct {
			store.Group
			Members []string `json:"members"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(raw, &gl); err != nil {
		t.Fatal(err)
	}
	sawBob := false
	for _, row := range gl.Groups {
		if row.ID == g.ID {
			for _, m := range row.Members {
				if m == "u-bob" {
					sawBob = true
				}
			}
		}
	}
	if !sawBob {
		t.Fatalf("groups listing missing member: %s", raw)
	}

	// Revoke + remove; visibility contracts again.
	if code, _ = doJSON(t, "DELETE", base+"/v1/admin/spaces/"+sp.ID+"/acl/"+g.ID, adminTok, nil, nil); code != 204 {
		t.Fatalf("remove acl: %d", code)
	}
	if code, _ = doJSON(t, "DELETE", base+"/v1/admin/groups/"+g.ID+"/members/u-bob", adminTok, nil, nil); code != 204 {
		t.Fatalf("remove member: %d", code)
	}
	vis, err = st.VisibleSpaces(ctx, "u-bob")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range vis {
		if s == sp.ID {
			t.Fatalf("revoked space still visible: %v", vis)
		}
	}
}

// TestAdminSpacesGuardrails: non-admin tokens are shut out entirely
// (403 by RequireScope), and personal spaces stay ungrantable through
// the API just like through the store.
func TestAdminSpacesGuardrails(t *testing.T) {
	base, adminTok, st := testServer(t)
	ctx := context.Background()

	if err := st.CreateUser(ctx, &store.User{ID: "u-carol", Name: "Carol", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	memberTok, err := st.CreateToken(ctx, "u-carol", "t", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, route := range []struct{ method, path string }{
		{"GET", "/v1/admin/spaces"},
		{"POST", "/v1/admin/spaces"},
		{"GET", "/v1/admin/groups"},
		{"POST", "/v1/admin/groups"},
		{"PUT", "/v1/admin/groups/g-x/members/u-carol"},
		{"DELETE", "/v1/admin/groups/g-x/members/u-carol"},
		{"PUT", "/v1/admin/spaces/sp-x/acl/g-x"},
		{"DELETE", "/v1/admin/spaces/sp-x/acl/g-x"},
	} {
		code, _ := doJSON(t, route.method, base+route.path, memberTok,
			map[string]any{"name": "x", "role": "member"}, nil)
		if code != http.StatusForbidden {
			t.Errorf("%s %s as member: %d, want 403", route.method, route.path, code)
		}
	}

	// Personal spaces are not grantable (store invariant surfaces as 400).
	code, raw := doJSON(t, "POST", base+"/v1/admin/groups", adminTok,
		map[string]any{"name": "grabbers"}, nil)
	if code != 201 {
		t.Fatalf("create group: %d %s", code, raw)
	}
	var g store.Group
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	code, raw = doJSON(t, "PUT",
		base+"/v1/admin/spaces/"+store.PersonalSpaceID("u-carol")+"/acl/"+g.ID,
		adminTok, map[string]any{"role": "member"}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("grant on personal space: %d %s, want 400", code, raw)
	}
}
