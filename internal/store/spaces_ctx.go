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
