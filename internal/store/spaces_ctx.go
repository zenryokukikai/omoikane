package store

// Request-scoped space visibility (issue #60, Phase 1 slice 2).
//
// The API layer resolves VisibleSpaces ONCE per authenticated request
// and installs the result on the context via WithVisibleSpaces. Store
// queries then narrow themselves through spaceCond / visibleEntryExists
// / requireVisibleEntry — all of which compose their SQL exclusively
// through SpaceFilter (the single composition point from slice 1).
//
// A context WITHOUT visibility installed is unrestricted: internal
// jobs, migrations, and store-level tests keep their full view. The
// HTTP surface is fail-closed regardless, because the API middleware
// installs visibility on every authenticated route (nil = admin's
// unrestricted view; an empty list sees nothing).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type visibleSpacesKey struct{}
type viewerUserKey struct{}

// visibility distinguishes "restricted to these spaces" from
// "unrestricted" without overloading nil slices inside the store.
type visibility struct {
	spaces       []string
	unrestricted bool
}

// WithVisibleSpaces installs the request's visible-space list on the
// context. spaces == nil means unrestricted (the admin contract); a
// non-nil empty slice means "sees nothing" (fail-closed).
func WithVisibleSpaces(ctx context.Context, spaces []string) context.Context {
	if spaces == nil {
		return context.WithValue(ctx, visibleSpacesKey{}, visibility{unrestricted: true})
	}
	return context.WithValue(ctx, visibleSpacesKey{}, visibility{spaces: spaces})
}

// visibleSpacesFrom reports the restriction on ctx. restricted == false
// means the caller may see every space.
func visibleSpacesFrom(ctx context.Context) (spaces []string, restricted bool) {
	v, ok := ctx.Value(visibleSpacesKey{}).(visibility)
	if !ok || v.unrestricted {
		return nil, false
	}
	return v.spaces, true
}

// VisibleSpacesFromContext exposes the restriction installed by
// WithVisibleSpaces for non-SQL consumers (the dashboard's space
// select UI). restricted == false means the viewer sees every space.
// Read-only: the SQL predicate itself still composes exclusively
// through spaceCond/SpaceFilter.
func VisibleSpacesFromContext(ctx context.Context) (spaces []string, restricted bool) {
	return visibleSpacesFrom(ctx)
}

// RequireVisibleSpace is the exported form of requireVisibleSpace for
// handlers validating a caller-supplied space id BEFORE composing it
// into a filter: ErrNotFound when the space does not exist OR lies
// outside the ctx's visible spaces (indistinguishable by design).
func (s *Store) RequireVisibleSpace(ctx context.Context, spaceID string) error {
	return requireVisibleSpace(ctx, s.db, spaceID)
}

// WithViewerUser records WHO the restricted view belongs to (the token's
// users.id). Installed by the same API middleware that installs
// WithVisibleSpaces; owner-scoped predicates (talk threads, slice 4)
// read it via talkThreadCond. Never consulted on unrestricted contexts.
func WithViewerUser(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, viewerUserKey{}, userID)
}

// viewerUserFrom returns the viewer's users.id, or "" when none was
// installed (fail-closed: an owner-scoped predicate with an empty viewer
// matches nobody's threads).
func viewerUserFrom(ctx context.Context) string {
	id, _ := ctx.Value(viewerUserKey{}).(string)
	return id
}

// talkThreadCond returns the SQL predicate restricting a chat_threads
// relation (alias, "" for bare columns) to rows the ctx's viewer may
// see: non-talk threads (librarian coordination) are shared; a thread
// with intent='talk' is a personal conversation and only its owner
// (created_by) may see it. Unrestricted contexts (admin scope, internal
// jobs, store-level tests) get "" — behaviour byte-identical to
// pre-slice-4. Composed with a LEFT JOIN the predicate stays true for
// messages without any thread row (NULL intent != 'talk').
func talkThreadCond(ctx context.Context, alias string) (clause string, args []any) {
	if _, restricted := visibleSpacesFrom(ctx); !restricted {
		return "", nil
	}
	intent, createdBy := "intent", "created_by"
	if alias != "" {
		intent, createdBy = alias+".intent", alias+".created_by"
	}
	return "(COALESCE(" + intent + ",'') != 'talk' OR " + createdBy + " = ?)",
		[]any{viewerUserFrom(ctx)}
}

// spaceCond returns the SQL predicate restricting an entries relation
// (qualified by alias) to the ctx's visible spaces. clause == "" means
// unrestricted — callers append nothing and existing SQL stays
// byte-identical (the behaviour-invariance guarantee for admin tokens
// and internal jobs).
func spaceCond(ctx context.Context, alias string) (clause string, args []any) {
	spaces, restricted := visibleSpacesFrom(ctx)
	if !restricted {
		return "", nil
	}
	return SpaceFilter(alias, spaces)
}

// visibleEntryExists returns an EXISTS predicate asserting that the
// entry referenced by entryIDCol (a compile-time column expression,
// e.g. "c.entry_id") is visible under ctx. Empty clause when
// unrestricted — the referencing row's FK already guarantees existence.
func visibleEntryExists(ctx context.Context, entryIDCol string) (clause string, args []any) {
	cond, condArgs := spaceCond(ctx, "e")
	if cond == "" {
		return "", nil
	}
	return "EXISTS (SELECT 1 FROM entries e WHERE e.id = " + entryIDCol + " AND " + cond + ")", condArgs
}

// queryRower is the QueryRowContext subset shared by *sql.DB and
// *sql.Tx, so visibility checks run inside or outside a transaction.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// requireVisibleEntry returns ErrNotFound unless the entry exists AND
// lies inside the ctx's visible spaces. Hidden and missing entries are
// deliberately indistinguishable (no existence oracle; the API maps
// ErrNotFound to 404, never 403). The message never echoes the entry id
// — some callers reach here via a case/comment id, and naming the
// underlying entry would disclose it (the leak-matrix test enforces
// this).
func requireVisibleEntry(ctx context.Context, q queryRower, entryID string) error {
	sqlQ := `SELECT 1 FROM entries e WHERE e.id = ?`
	args := []any{entryID}
	if cond, condArgs := spaceCond(ctx, "e"); cond != "" {
		sqlQ += " AND " + cond
		args = append(args, condArgs...)
	}
	var one int
	err := q.QueryRowContext(ctx, sqlQ, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		// Bare sentinel: callers historically compare with == ErrNotFound,
		// and a wrapped message must never name the entry anyway.
		return ErrNotFound
	}
	return err
}

// requireVisibleAggregate returns ErrNotFound unless the aggregate row
// (situations / incident_clusters / hierarchy_nodes / use_cases /
// attachments — table is always a compile-time constant, never user
// input) exists AND lies inside the ctx's visible spaces. Unrestricted
// contexts skip the space predicate entirely, so internal jobs and
// store-level tests keep their pre-slice-3 behaviour byte-identically.
func requireVisibleAggregate(ctx context.Context, q queryRower, table, id string) error {
	sqlQ := `SELECT 1 FROM ` + table + ` a WHERE a.id = ?`
	args := []any{id}
	if cond, condArgs := spaceCond(ctx, "a"); cond != "" {
		sqlQ += " AND " + cond
		args = append(args, condArgs...)
	}
	var one int
	err := q.QueryRowContext(ctx, sqlQ, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// requireSameSpaceLink is the single-space aggregate invariant (design
// v2): adding an entry to an aggregate requires the aggregate to be
// visible AND the entry to live in the aggregate's own space. The space
// equality is enforced even for unrestricted contexts — it is an
// integrity invariant, not just an ACL — while the visibility predicate
// composes only when the ctx is restricted. Any failure (missing
// aggregate, missing entry, hidden either, cross-space pair) collapses
// into ErrNotFound: indistinguishable by design.
func requireSameSpaceLink(ctx context.Context, q queryRower, table, aggID, entryID string) error {
	sqlQ := `SELECT 1 FROM ` + table + ` a JOIN entries e ON e.id = ?
		WHERE a.id = ? AND e.space_id = a.space_id`
	args := []any{entryID, aggID}
	if cond, condArgs := spaceCond(ctx, "a"); cond != "" {
		sqlQ += " AND " + cond
		args = append(args, condArgs...)
	}
	var one int
	err := q.QueryRowContext(ctx, sqlQ, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// requireVisibleSpace returns ErrNotFound unless the space exists AND
// is inside the ctx's visible spaces. Used by CreateEntry: a space the
// caller cannot see must be indistinguishable from one that does not
// exist.
func requireVisibleSpace(ctx context.Context, q queryRower, spaceID string) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM spaces WHERE id = ?`, spaceID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: space %q", ErrNotFound, spaceID)
	}
	if err != nil {
		return err
	}
	if spaces, restricted := visibleSpacesFrom(ctx); restricted {
		for _, sp := range spaces {
			if sp == spaceID {
				return nil
			}
		}
		return fmt.Errorf("%w: space %q", ErrNotFound, spaceID)
	}
	return nil
}
