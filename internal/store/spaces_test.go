package store

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// ============================================================
// VisibleSpaces — every branch of the single source of truth
// ============================================================

func TestVisibleSpacesEmptyUserIDFailsClosed(t *testing.T) {
	s := newTestStore(t)
	got, err := s.VisibleSpaces(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("empty userID must see nothing, got %v", got)
	}
}

func TestVisibleSpacesPersonalAndInternal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, &User{ID: "u-vis1", Name: "Vis"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.VisibleSpaces(ctx, "u-vis1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{SpaceInternal, PersonalSpaceID("u-vis1")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestVisibleSpacesExternalRoleGetsPersonalOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, &User{ID: "u-ext", Name: "Ext", Role: RoleExternal}); err != nil {
		t.Fatal(err)
	}
	got, err := s.VisibleSpaces(ctx, "u-ext")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{PersonalSpaceID("u-ext")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("external user must see only personal space, got %v", got)
	}
}

func TestVisibleSpacesGroupGrant(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, &User{ID: "u-in", Name: "In"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, &User{ID: "u-out", Name: "Out"}); err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateGroup(ctx, "sec-project")
	if err != nil {
		t.Fatal(err)
	}
	sp, err := s.CreateSpace(ctx, "secret space")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddGroupMember(ctx, g.ID, "u-in"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSpaceACL(ctx, sp.ID, g.ID, SpaceRoleMember); err != nil {
		t.Fatal(err)
	}

	got, err := s.VisibleSpaces(ctx, "u-in")
	if err != nil {
		t.Fatal(err)
	}
	if !containsStr(got, sp.ID) || !containsStr(got, SpaceInternal) ||
		!containsStr(got, PersonalSpaceID("u-in")) || len(got) != 3 {
		t.Fatalf("member visibility wrong: %v", got)
	}

	// Non-member of the group must not see the granted space.
	got, err = s.VisibleSpaces(ctx, "u-out")
	if err != nil {
		t.Fatal(err)
	}
	if containsStr(got, sp.ID) {
		t.Fatalf("non-member must not see %s: %v", sp.ID, got)
	}

	// Revoking membership removes the space from the member's view.
	if err := s.RemoveGroupMember(ctx, g.ID, "u-in"); err != nil {
		t.Fatal(err)
	}
	got, err = s.VisibleSpaces(ctx, "u-in")
	if err != nil {
		t.Fatal(err)
	}
	if containsStr(got, sp.ID) {
		t.Fatalf("removed member must not see %s: %v", sp.ID, got)
	}
}

func TestVisibleSpacesUnknownUserFailsClosed(t *testing.T) {
	s := newTestStore(t)
	got, err := s.VisibleSpaces(context.Background(), "u-nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unknown userID must see nothing, got %v", got)
	}
}

// TestSetSpaceACLRejectsPersonalSpace pins the leak the third-party
// review caught: granting a group on someone's personal space would
// expose it to every group member. The grant must be refused and the
// would-be reader's visibility must not include the victim's space.
func TestSetSpaceACLRejectsPersonalSpace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, &User{ID: "u-victim", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, &User{ID: "u-reader", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateGroup(ctx, "snoopers")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddGroupMember(ctx, g.ID, "u-reader"); err != nil {
		t.Fatal(err)
	}

	err = s.SetSpaceACL(ctx, PersonalSpaceID("u-victim"), g.ID, SpaceRoleMember)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("grant on personal space must be ErrInvalidInput, got %v", err)
	}

	got, err := s.VisibleSpaces(ctx, "u-reader")
	if err != nil {
		t.Fatal(err)
	}
	if containsStr(got, PersonalSpaceID("u-victim")) {
		t.Fatalf("reader must not see victim's personal space: %v", got)
	}
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// ============================================================
// User-creation hook — every creation path provisions spaces
// ============================================================

func TestRegisterAgentProvisionsSpaces(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	reg, err := s.RegisterAgent(ctx, "spacebot", "")
	if err != nil {
		t.Fatal(err)
	}
	assertProvisioned(t, s, reg.AgentUser.ID)
}

func TestRedeemAgentInvitationProvisionsSpaces(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, &User{ID: "u-inviter", Name: "Inviter"}); err != nil {
		t.Fatal(err)
	}
	inv, err := s.CreateAgentInvitation(ctx, "u-inviter", "", "")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := s.RedeemAgentInvitation(ctx, inv.Code, "invited-bot", "")
	if err != nil {
		t.Fatal(err)
	}
	assertProvisioned(t, s, reg.AgentUser.ID)
}

func TestProvisionGoogleUserProvisionsSpaces(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u, err := s.ProvisionGoogleUser(ctx, "g@example.com", "sub-123", "G User", "")
	if err != nil {
		t.Fatal(err)
	}
	assertProvisioned(t, s, u.ID)
}

func assertProvisioned(t *testing.T, s *Store, userID string) {
	t.Helper()
	ctx := context.Background()
	got, err := s.VisibleSpaces(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsStr(got, SpaceInternal) {
		t.Fatalf("user %s should be in group internal (spaces %v)", userID, got)
	}
	sp, err := s.GetSpace(ctx, PersonalSpaceID(userID))
	if err != nil {
		t.Fatalf("personal space missing for %s: %v", userID, err)
	}
	if sp.Kind != SpaceKindPersonal {
		t.Fatalf("personal space kind = %q", sp.Kind)
	}
}

// ============================================================
// Group / Space / ACL CRUD round-trips
// ============================================================

func TestGroupCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateGroup(ctx, "  "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank name: %v", err)
	}
	g, err := s.CreateGroup(ctx, "team-a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(g.ID, "g-") || len(g.ID) != 10 {
		t.Fatalf("group id format: %q", g.ID)
	}
	if _, err := s.CreateGroup(ctx, "team-a"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate name: %v", err)
	}

	groups, err := s.ListGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// migration seeds group 'internal'; we added team-a.
	if len(groups) != 2 || groups[0].Name != "internal" || groups[1].Name != "team-a" {
		t.Fatalf("groups: %+v", groups)
	}

	if err := s.CreateUser(ctx, &User{ID: "u-m1", Name: "M1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddGroupMember(ctx, g.ID, "u-m1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddGroupMember(ctx, g.ID, "u-m1"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate member: %v", err)
	}
	if err := s.AddGroupMember(ctx, g.ID, "u-ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
	if err := s.AddGroupMember(ctx, "g-missing", "u-m1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing group: %v", err)
	}
	if err := s.AddGroupMember(ctx, "", "u-m1"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank group id: %v", err)
	}

	members, err := s.ListGroupMembers(ctx, g.ID)
	if err != nil || !reflect.DeepEqual(members, []string{"u-m1"}) {
		t.Fatalf("members: %v err=%v", members, err)
	}
	if _, err := s.ListGroupMembers(ctx, "g-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("list missing group: %v", err)
	}

	if err := s.RemoveGroupMember(ctx, g.ID, "u-m1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveGroupMember(ctx, g.ID, "u-m1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove absent member: %v", err)
	}
}

func TestSpaceACLCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateSpace(ctx, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("blank name: %v", err)
	}
	sp, err := s.CreateSpace(ctx, "proj-x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sp.ID, "sp-") || sp.Kind != SpaceKindRestricted {
		t.Fatalf("space: %+v", sp)
	}

	spaces, err := s.ListSpaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// migration seeds space 'internal'; we added sp.
	if len(spaces) != 2 || spaces[0].ID != SpaceInternal {
		t.Fatalf("spaces: %+v", spaces)
	}
	if _, err := s.GetSpace(ctx, "sp-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}

	g, err := s.CreateGroup(ctx, "team-b")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetSpaceACL(ctx, sp.ID, g.ID, "owner"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad role: %v", err)
	}
	if err := s.SetSpaceACL(ctx, "sp-missing", g.ID, SpaceRoleMember); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing space: %v", err)
	}
	if err := s.SetSpaceACL(ctx, sp.ID, "g-missing", SpaceRoleMember); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing group: %v", err)
	}
	if err := s.SetSpaceACL(ctx, sp.ID, g.ID, SpaceRoleMember); err != nil {
		t.Fatal(err)
	}
	// Upsert: same pair, new role.
	if err := s.SetSpaceACL(ctx, sp.ID, g.ID, SpaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	acl, err := s.ListSpaceACL(ctx, sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(acl) != 1 || acl[0].Role != SpaceRoleAdmin || acl[0].GroupID != g.ID {
		t.Fatalf("acl: %+v", acl)
	}
	if _, err := s.ListSpaceACL(ctx, "sp-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("list missing space: %v", err)
	}

	if err := s.RemoveSpaceACL(ctx, sp.ID, g.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveSpaceACL(ctx, sp.ID, g.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove absent acl: %v", err)
	}
}

// ============================================================
// SpaceFilter — the single SQL composition point
// ============================================================

func TestSpaceFilter(t *testing.T) {
	clause, args := SpaceFilter("", nil)
	if clause != "1=0" || args != nil {
		t.Fatalf("empty must fail closed: %q %v", clause, args)
	}
	clause, args = SpaceFilter("e", []string{"internal", "sp-1"})
	if clause != "e.space_id IN (?,?)" {
		t.Fatalf("clause: %q", clause)
	}
	if !reflect.DeepEqual(args, []any{"internal", "sp-1"}) {
		t.Fatalf("args: %v", args)
	}
	clause, _ = SpaceFilter("", []string{"internal"})
	if clause != "space_id IN (?)" {
		t.Fatalf("bare clause: %q", clause)
	}
}

// TestSpaceFilterExecutes proves the built predicate is valid SQL
// against the real entries table and actually partitions rows.
func TestSpaceFilterExecutes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, &Project{ID: "pf", Name: "PF"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateEntry(ctx, &Entry{
		ProjectID: "pf", Type: "lesson", Title: "t", Body: "b",
	}); err != nil {
		t.Fatal(err)
	}

	count := func(spaces []string) int {
		clause, args := SpaceFilter("e", spaces)
		var n int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM entries e WHERE `+clause, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count([]string{SpaceInternal}); got != 1 {
		t.Fatalf("internal filter: %d", got)
	}
	if got := count([]string{"sp-elsewhere"}); got != 0 {
		t.Fatalf("other-space filter: %d", got)
	}
	if got := count(nil); got != 0 {
		t.Fatalf("fail-closed filter: %d", got)
	}
}

// ============================================================
// Migration 031 backfill on a pre-existing database
// ============================================================

// partialMigrationsFS returns the embedded migrations limited to
// version <= maxVersion, so a test can build a database "as of" an
// older schema and then let the real FS apply the remainder.
func partialMigrationsFS(t *testing.T, maxVersion int) fs.FS {
	t.Helper()
	out := fstest.MapFS{}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		under := strings.IndexByte(e.Name(), '_')
		v, err := strconv.Atoi(e.Name()[:under])
		if err != nil {
			t.Fatalf("bad migration name %q", e.Name())
		}
		if v > maxVersion {
			continue
		}
		data, err := fs.ReadFile(migrationsFS, "migrations/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		out["migrations/"+e.Name()] = &fstest.MapFile{Data: data}
	}
	return out
}

func TestMigration031BackfillsExistingUsersAndEntries(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	// Phase 1: database as of migration 029, with pre-existing users
	// (human + agent) and an entry — inserted raw, since the store's
	// provisioning hook must not exist yet in this world.
	orig := migrationFS
	t.Cleanup(func() { migrationFS = orig })
	migrationFS = partialMigrationsFS(t, 29)
	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := s1.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO users(id, name, role) VALUES ('u-old-h', 'Old Human', 'member')`)
	mustExec(`INSERT INTO users(id, name, role) VALUES ('u-old-a', 'old-agent', 'agent')`)
	mustExec(`INSERT INTO projects(id, name) VALUES ('pm', 'PM')`)
	mustExec(`INSERT INTO entries(id, project_id, type, title, body) VALUES ('e-old', 'pm', 'lesson', 'T', 'B')`)
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	// Phase 2: reopen with the full migration set — 031 applies and
	// backfills.
	migrationFS = orig
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	for _, uid := range []string{"u-old-h", "u-old-a"} {
		got, err := s2.VisibleSpaces(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{SpaceInternal, PersonalSpaceID(uid)}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("backfilled visibility for %s: got %v want %v", uid, got, want)
		}
		sp, err := s2.GetSpace(ctx, PersonalSpaceID(uid))
		if err != nil || sp.Kind != SpaceKindPersonal {
			t.Fatalf("personal space for %s: %+v err=%v", uid, sp, err)
		}
	}

	var spaceID string
	if err := s2.db.QueryRowContext(ctx,
		`SELECT space_id FROM entries WHERE id = 'e-old'`).Scan(&spaceID); err != nil {
		t.Fatal(err)
	}
	if spaceID != SpaceInternal {
		t.Fatalf("pre-existing entry space = %q, want internal", spaceID)
	}
}
