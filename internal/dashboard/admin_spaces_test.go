package dashboard

// /admin/spaces — the space/group management console (issue #60,
// Phase 1 slice 5). Effects are verified against the store (primary
// evidence), not just the rendered HTML.

import (
	"context"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

func TestAdminSpacesPageAndActions(t *testing.T) {
	srv, st, adminTok := mountAuthed(t) // alice, token carries admin scope
	ctx := context.Background()
	if err := st.CreateUser(ctx, &store.User{ID: "bob", Name: "Bob", Role: "member"}); err != nil {
		t.Fatal(err)
	}

	// The page renders the reserved space and the create controls.
	code, body := get(t, srv, "/admin/spaces", adminTok)
	bs := string(body)
	if code != 200 || !strings.Contains(bs, "internal") ||
		!strings.Contains(bs, "Create a restricted space") {
		t.Fatalf("admin page initial render: code=%d", code)
	}
	// Personal spaces are listed but offer no grant controls.
	if !strings.Contains(bs, "p-alice") || !strings.Contains(bs, "本人のみ") {
		t.Fatalf("personal space row missing or grantable")
	}

	// Create a group and a space through the forms.
	if code := postForm(t, srv, "/admin/groups/create", adminTok,
		map[string]string{"name": "dev-team"}); code != 303 {
		t.Fatalf("group create: code=%d", code)
	}
	groups, err := st.ListGroups(ctx)
	if err != nil || len(groups) != 2 { // internal + dev-team
		t.Fatalf("groups after create: %v %v", groups, err)
	}
	var devTeam *store.Group
	for _, g := range groups {
		if g.Name == "dev-team" {
			devTeam = g
		}
	}
	if devTeam == nil {
		t.Fatal("dev-team not created")
	}

	if code := postForm(t, srv, "/admin/spaces/create", adminTok,
		map[string]string{"name": "project-x"}); code != 303 {
		t.Fatalf("space create: code=%d", code)
	}
	var projX *store.Space
	spaces, err := st.ListSpaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range spaces {
		if sp.Name == "project-x" {
			projX = sp
		}
	}
	if projX == nil || projX.Kind != store.SpaceKindRestricted {
		t.Fatalf("project-x not created as restricted: %+v", projX)
	}

	// Membership + grant → bob's resolved visibility gains the space.
	if code := postForm(t, srv, "/admin/groups/"+devTeam.ID+"/members/add", adminTok,
		map[string]string{"user_id": "bob"}); code != 303 {
		t.Fatalf("member add: code=%d", code)
	}
	if code := postForm(t, srv, "/admin/spaces/"+projX.ID+"/acl", adminTok,
		map[string]string{"group_id": devTeam.ID, "role": "member"}); code != 303 {
		t.Fatalf("acl set: code=%d", code)
	}
	vis, err := st.VisibleSpaces(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(vis, projX.ID) {
		t.Fatalf("grant did not reach bob's visibility: %v", vis)
	}

	// The page now shows the grant and the membership so the operator
	// can read table → action correspondence.
	_, body = get(t, srv, "/admin/spaces", adminTok)
	bs = string(body)
	for _, want := range []string{"project-x", "dev-team", "bob", "revoke", "remove"} {
		if !strings.Contains(bs, want) {
			t.Errorf("admin page missing %q after grant", want)
		}
	}

	// Revoke + remove → visibility contracts again.
	if code := postForm(t, srv, "/admin/spaces/"+projX.ID+"/acl/remove", adminTok,
		map[string]string{"group_id": devTeam.ID}); code != 303 {
		t.Fatalf("acl remove: code=%d", code)
	}
	if code := postForm(t, srv, "/admin/groups/"+devTeam.ID+"/members/remove", adminTok,
		map[string]string{"user_id": "bob"}); code != 303 {
		t.Fatalf("member remove: code=%d", code)
	}
	vis, err = st.VisibleSpaces(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if contains(vis, projX.ID) {
		t.Fatalf("revoked space still visible to bob: %v", vis)
	}

	// A grant on a personal space bounces with an error banner and no
	// store effect (the store invariant, surfaced through the form path).
	if code := postForm(t, srv, "/admin/spaces/p-alice/acl", adminTok,
		map[string]string{"group_id": devTeam.ID, "role": "member"}); code != 303 {
		t.Fatalf("personal-space grant attempt: code=%d", code)
	}
	if acl, err := st.ListSpaceACL(ctx, "p-alice"); err != nil || len(acl) != 0 {
		t.Fatalf("personal space gained an ACL row: %v %v", acl, err)
	}
}

func TestAdminSpacesRefusesNonAdmin(t *testing.T) {
	srv, st, _ := mountAuthed(t)
	ctx := context.Background()
	if err := st.CreateUser(ctx, &store.User{ID: "carol", Name: "Carol", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	memberTok, err := st.CreateToken(ctx, "carol", "t", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Page: readable banner, no management controls.
	code, body := get(t, srv, "/admin/spaces", memberTok)
	bs := string(body)
	if code != 200 || !strings.Contains(bs, "Only admins") {
		t.Fatalf("non-admin page: code=%d", code)
	}
	if strings.Contains(bs, "Create a restricted space") {
		t.Fatalf("management controls rendered for a non-admin")
	}

	// Every form POST: 403, no store effect.
	for _, p := range []string{
		"/admin/spaces/create", "/admin/groups/create",
		"/admin/groups/internal/members/add", "/admin/groups/internal/members/remove",
		"/admin/spaces/internal/acl", "/admin/spaces/internal/acl/remove",
	} {
		if code := postForm(t, srv, p, memberTok, map[string]string{
			"name": "evil", "user_id": "carol", "group_id": "internal", "role": "admin",
		}); code != 403 {
			t.Errorf("POST %s as non-admin: code=%d, want 403", p, code)
		}
	}
	if spaces, _ := st.ListSpaces(ctx); len(spaces) != 3 { // internal + p-alice + p-carol
		t.Fatalf("non-admin POST changed the space table: %v", spaces)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
