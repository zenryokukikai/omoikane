package store

// entry_scan.go is the package-wide read layer for `entries` rows: the
// canonical column list plus the scanners that consume it. It is NOT
// private to entries.go — search.go (SearchFTS / scanEntryWithRank) and
// librarian_progress.go build their queries on entrySelectSQL and feed
// the rows to scanEntry.
//
// Column order is a CONTRACT: scanEntry and scanEntryWithRank scan
// positionally in exactly the order of entryColumnsSQL. Never reorder
// or extend the list without updating every scanner in the same change.

import (
	"context"
	"database/sql"
	"encoding/json"
)

// entryColumnsSQL is the one column-order contract for reading an
// `entries` row (table alias `e`). entrySelectSQL and search.go's
// ranked SELECT are both built from it.
const entryColumnsSQL = `
	e.id, e.project_id, e.type, e.title, e.status,
	COALESCE(e.symptom,''), COALESCE(e.root_cause,''), COALESCE(e.resolution,''),
	COALESCE(e.prohibited,''),
	COALESCE(e.attempted_approaches,''), COALESCE(e.observed_behavior,''),
	COALESCE(e.hypotheses,''),
	e.body, e.body_format,
	COALESCE(e.scope,''), COALESCE(e.metadata,''),
	e.valid_from, e.valid_to,
	COALESCE(e.superseded_by,''), COALESCE(e.invalidation_reason,''),
	e.enrichment_version, e.enrichment_at,
	e.created_at, e.updated_at,
	COALESCE(e.created_by,''), COALESCE(e.created_by_role,''),
	e.version, e.space_id`

const entrySelectSQL = `SELECT` + entryColumnsSQL + `
FROM entries e`

type scanner interface {
	Scan(dest ...any) error
}

// rawOrNil normalises an empty TEXT column value (COALESCE'd NULL) to a
// nil json.RawMessage so `omitempty` drops the field from API responses
// cleanly. (An empty but non-nil json.RawMessage marshals as zero
// bytes, which breaks the surrounding JSON encoding.)
func rawOrNil(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

func scanEntry(r scanner) (*Entry, error) {
	var (
		e            Entry
		validTo      sql.NullTime
		enrichmentAt sql.NullTime
		// Scope and Metadata live in TEXT columns COALESCE'd to '' for
		// NULL. We scan into a string and post-process via rawOrNil.
		scopeRaw string
		metaRaw  string
	)
	err := r.Scan(&e.ID, &e.ProjectID, &e.Type, &e.Title, &e.Status,
		&e.Symptom, &e.RootCause, &e.Resolution, &e.Prohibited,
		&e.AttemptedApproaches, &e.ObservedBehavior, &e.Hypotheses,
		&e.Body, &e.BodyFormat,
		&scopeRaw, &metaRaw,
		&e.ValidFrom, &validTo,
		&e.SupersededBy, &e.InvalidationReason,
		&e.EnrichmentVersion, &enrichmentAt,
		&e.CreatedAt, &e.UpdatedAt,
		&e.CreatedBy, &e.CreatedByRole,
		&e.Version, &e.SpaceID)
	if err != nil {
		return nil, translateErr(err)
	}
	e.Scope = rawOrNil(scopeRaw)
	e.Metadata = rawOrNil(metaRaw)
	if validTo.Valid {
		t := validTo.Time
		e.ValidTo = &t
	}
	if enrichmentAt.Valid {
		t := enrichmentAt.Time
		e.EnrichmentAt = &t
	}
	return &e, nil
}

func scanEntryRow(r *sql.Row) (*Entry, error) { return scanEntry(r) }

func loadEntryTx(ctx context.Context, tx *sql.Tx, id string) (*Entry, error) {
	// Same visibility narrowing as GetEntry, so every write path that
	// loads-then-mutates (UpdateEntry, SoftDeleteEntry) 404s on entries
	// outside the ctx's visible spaces.
	q := entrySelectSQL + ` WHERE e.id = ?`
	args := []any{id}
	if cond, condArgs := spaceCond(ctx, "e"); cond != "" {
		q += " AND " + cond
		args = append(args, condArgs...)
	}
	row := tx.QueryRowContext(ctx, q, args...)
	return scanEntry(row)
}
