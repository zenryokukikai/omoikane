package store

// Spaces / groups read-ACL (issue #60, Phase 1 slice 1).
//
// A space is a visibility boundary; project_id stays as the category
// WITHIN a space. Visibility is resolved ONLY by VisibleSpaces (the
// single source of truth) and the SQL predicate is built ONLY by
// SpaceFilter (the single SQL composition point). No other code may
// hand-write space conditions — that rule is what keeps "gate on top
// of gate" implementations from ever appearing.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Reserved identifiers and enums (deliberate reserved words, design v2).
const (
	// SpaceInternal is the reserved space every member of GroupInternal
	// can read. All pre-migration entries live here.
	SpaceInternal = "internal"
	// GroupInternal is the reserved group users auto-join at creation
	// (unless their role is "external").
	GroupInternal = "internal"

	SpaceKindInternal   = "internal"
	SpaceKindRestricted = "restricted"
	SpaceKindPersonal   = "personal"

	SpaceRoleAdmin  = "admin"
	SpaceRoleMember = "member"

	// RoleExternal marks accounts outside the organisation; they are
	// NOT auto-joined to GroupInternal. No such users exist yet — the
	// constant pins the contract for when they do.
	RoleExternal = "external"
)

// PersonalSpaceID returns the id of a user's personal space. The ACL of
// a personal space is implicit (the owner only, no space_acl rows).
func PersonalSpaceID(userID string) string { return "p-" + userID }

type Group struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Space struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"` // internal | restricted | personal
	CreatedAt time.Time `json:"created_at"`
}

// SpaceACL is one grant: members of GroupID may read SpaceID; role
// 'admin' additionally marks who may manage the space (enforced by the
// API layer in later slices — the store just persists it).
type SpaceACL struct {
	SpaceID string `json:"space_id"`
	GroupID string `json:"group_id"`
	Role    string `json:"role"` // admin | member
}

// execer is the ExecContext subset shared by *sql.DB and *sql.Tx, so the
// user-creation hook can run inside the caller's transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ============================================================
// User-creation hook
// ============================================================

// ensureUserSpaces is the single user-provisioning hook: every path
// that inserts a users row (CreateUser, RegisterAgent,
// RedeemAgentInvitation) MUST call it in the same transaction. It
// joins the user to GroupInternal (unless role is external) and
// creates the personal space. Idempotent (INSERT OR IGNORE) so
// re-provisioning an existing identity is harmless.
func ensureUserSpaces(ctx context.Context, ex execer, userID, name, role string) error {
	if userID == "" {
		return fmt.Errorf("%w: user id required", ErrInvalidInput)
	}
	if role != RoleExternal {
		if _, err := ex.ExecContext(ctx, `
			INSERT OR IGNORE INTO group_members(group_id, user_id)
			VALUES (?, ?)`, GroupInternal, userID); err != nil {
			return translateErr(err)
		}
	}
	if name == "" {
		name = userID
	}
	if _, err := ex.ExecContext(ctx, `
		INSERT OR IGNORE INTO spaces(id, name, kind)
		VALUES (?, ?, ?)`, PersonalSpaceID(userID), name, SpaceKindPersonal); err != nil {
		return translateErr(err)
	}
	return nil
}

// ============================================================
// Visibility resolution — the single source of truth
// ============================================================

// VisibleSpaces returns every space id the user may read:
//
//  1. the personal space p-<userID>
//  2. SpaceInternal, iff the user is a member of GroupInternal
//  3. every space granted to any of the user's groups via space_acl
//
// userID == "" returns an empty slice (fail-closed: an anonymous or
// user-less principal sees nothing; token-level widening such as the
// admin scope is layered on by the API in later slices, never here).
// Resolve once per request and carry the result — do not re-derive
// visibility anywhere else.
func (s *Store) VisibleSpaces(ctx context.Context, userID string) ([]string, error) {
	if userID == "" {
		return []string{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM spaces WHERE id = ?
		UNION
		SELECT ? FROM group_members WHERE group_id = ? AND user_id = ?
		UNION
		SELECT sa.space_id
		FROM space_acl sa
		JOIN group_members gm ON gm.group_id = sa.group_id
		WHERE gm.user_id = ?
		ORDER BY 1`,
		PersonalSpaceID(userID),
		SpaceInternal, GroupInternal, userID,
		userID)
	if err != nil {
		return nil, err
	}
	return collectStrings(rows)
}

// SpaceFilter builds the SQL predicate restricting a query to the given
// visible spaces. This is the ONLY place a space condition may be
// composed — later slices pass VisibleSpaces output straight in.
// alias qualifies the column ("e" -> "e.space_id"); "" leaves it bare.
// An empty space list yields the always-false predicate (fail-closed).
func SpaceFilter(alias string, spaces []string) (clause string, args []any) {
	col := "space_id"
	if alias != "" {
		col = alias + ".space_id"
	}
	if len(spaces) == 0 {
		return "1=0", nil
	}
	args = make([]any, len(spaces))
	for i, sp := range spaces {
		args[i] = sp
	}
	return col + " IN (?" + strings.Repeat(",?", len(spaces)-1) + ")", args
}

// ============================================================
// Group CRUD
// ============================================================

func scanGroup(r rowScanner, g *Group) error {
	return r.Scan(&g.ID, &g.Name, &g.CreatedAt)
}

// CreateGroup mints a g-<8hex> group. Name must be unique
// (ErrAlreadyExists on collision).
func (s *Store) CreateGroup(ctx context.Context, name string) (*Group, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: name required", ErrInvalidInput)
	}
	id := newLibrarianID("g")
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO groups(id, name) VALUES (?, ?)`, id, name); err != nil {
		return nil, translateErr(err)
	}
	return s.getGroup(ctx, id)
}

func (s *Store) getGroup(ctx context.Context, id string) (*Group, error) {
	var g Group
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, created_at FROM groups WHERE id = ?`, id).
		Scan(&g.ID, &g.Name, &g.CreatedAt)
	if err != nil {
		return nil, translateErr(err)
	}
	return &g, nil
}

func (s *Store) ListGroups(ctx context.Context) ([]*Group, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, created_at FROM groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return mapRows[*Group](rows, func(c rowScanner, g **Group) error {
		*g = &Group{}
		return scanGroup(c, *g)
	})
}

// AddGroupMember adds a user to a group. Both must exist (ErrNotFound
// otherwise); a duplicate membership is ErrAlreadyExists.
func (s *Store) AddGroupMember(ctx context.Context, groupID, userID string) error {
	if groupID == "" || userID == "" {
		return fmt.Errorf("%w: group id and user id required", ErrInvalidInput)
	}
	if err := s.requireRow(ctx, "groups", groupID); err != nil {
		return err
	}
	if err := s.requireRow(ctx, "users", userID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO group_members(group_id, user_id) VALUES (?, ?)`,
		groupID, userID)
	return translateErr(err)
}

// RemoveGroupMember removes a membership; ErrNotFound if it wasn't there.
func (s *Store) RemoveGroupMember(ctx context.Context, groupID, userID string) error {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM group_members WHERE group_id = ? AND user_id = ?`,
		groupID, userID)
	if err != nil {
		return translateErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListGroupMembers returns the member user ids of a group (ErrNotFound
// if the group doesn't exist).
func (s *Store) ListGroupMembers(ctx context.Context, groupID string) ([]string, error) {
	if err := s.requireRow(ctx, "groups", groupID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id FROM group_members WHERE group_id = ? ORDER BY user_id`,
		groupID)
	if err != nil {
		return nil, err
	}
	return collectStrings(rows)
}

// ============================================================
// Space CRUD + ACL
// ============================================================

func scanSpace(r rowScanner, sp *Space) error {
	return r.Scan(&sp.ID, &sp.Name, &sp.Kind, &sp.CreatedAt)
}

// CreateSpace mints an sp-<8hex> restricted space. Only
// kind=restricted is creatable here by design: 'internal' exists from
// the migration and personal spaces come from the user-creation hook —
// a single provisioning contract per kind.
func (s *Store) CreateSpace(ctx context.Context, name string) (*Space, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: name required", ErrInvalidInput)
	}
	id := newLibrarianID("sp")
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO spaces(id, name, kind) VALUES (?, ?, ?)`,
		id, name, SpaceKindRestricted); err != nil {
		return nil, translateErr(err)
	}
	return s.GetSpace(ctx, id)
}

func (s *Store) GetSpace(ctx context.Context, id string) (*Space, error) {
	var sp Space
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, kind, created_at FROM spaces WHERE id = ?`, id).
		Scan(&sp.ID, &sp.Name, &sp.Kind, &sp.CreatedAt)
	if err != nil {
		return nil, translateErr(err)
	}
	return &sp, nil
}

func (s *Store) ListSpaces(ctx context.Context) ([]*Space, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, kind, created_at FROM spaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return mapRows[*Space](rows, func(c rowScanner, sp **Space) error {
		*sp = &Space{}
		return scanSpace(c, *sp)
	})
}

// SetSpaceACL grants (or updates) a group's role on a space. Upsert:
// setting a new role for an existing (space, group) pair overwrites it.
func (s *Store) SetSpaceACL(ctx context.Context, spaceID, groupID, role string) error {
	if role != SpaceRoleAdmin && role != SpaceRoleMember {
		return fmt.Errorf("%w: role must be admin|member", ErrInvalidInput)
	}
	if err := s.requireRow(ctx, "spaces", spaceID); err != nil {
		return err
	}
	if err := s.requireRow(ctx, "groups", groupID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO space_acl(space_id, group_id, role) VALUES (?, ?, ?)
		ON CONFLICT(space_id, group_id) DO UPDATE SET role = excluded.role`,
		spaceID, groupID, role)
	return translateErr(err)
}

// RemoveSpaceACL revokes a group's grant on a space; ErrNotFound if the
// grant wasn't there.
func (s *Store) RemoveSpaceACL(ctx context.Context, spaceID, groupID string) error {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM space_acl WHERE space_id = ? AND group_id = ?`,
		spaceID, groupID)
	if err != nil {
		return translateErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSpaceACL returns all grants on a space (ErrNotFound if the space
// doesn't exist).
func (s *Store) ListSpaceACL(ctx context.Context, spaceID string) ([]*SpaceACL, error) {
	if err := s.requireRow(ctx, "spaces", spaceID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT space_id, group_id, role FROM space_acl
		WHERE space_id = ? ORDER BY group_id`, spaceID)
	if err != nil {
		return nil, err
	}
	return mapRows[*SpaceACL](rows, func(c rowScanner, a **SpaceACL) error {
		*a = &SpaceACL{}
		return c.Scan(&(*a).SpaceID, &(*a).GroupID, &(*a).Role)
	})
}

// requireRow maps "referenced id doesn't exist" to ErrNotFound before a
// write, so callers see the store's sentinel instead of an opaque
// FOREIGN KEY error. table is always a compile-time constant here.
func (s *Store) requireRow(ctx context.Context, table, id string) error {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM `+table+` WHERE id = ?`, id).Scan(&one)
	if err != nil {
		return translateErr(err)
	}
	return nil
}
