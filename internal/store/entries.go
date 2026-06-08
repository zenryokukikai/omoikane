package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// EntryHistory mirrors one row of entry_history (a full snapshot of all
// mutable fields at the time of a write).
type EntryHistory struct {
	EntryID             string     `json:"entry_id"`
	Version             int        `json:"version"`
	Title               string     `json:"title"`
	Status              string     `json:"status"`
	Symptom             string     `json:"symptom,omitempty"`
	RootCause           string     `json:"root_cause,omitempty"`
	Resolution          string     `json:"resolution,omitempty"`
	Prohibited          string     `json:"prohibited,omitempty"`
	AttemptedApproaches string     `json:"attempted_approaches,omitempty"`
	ObservedBehavior    string     `json:"observed_behavior,omitempty"`
	Hypotheses          string     `json:"hypotheses,omitempty"`
	Body                string     `json:"body"`
	BodyFormat          string     `json:"body_format"`
	// See Entry.Scope/Metadata for rationale — raw JSON, not strings.
	Scope               json.RawMessage `json:"scope,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
	ValidFrom           time.Time  `json:"valid_from"`
	ValidTo             *time.Time `json:"valid_to,omitempty"`
	SupersededBy        string     `json:"superseded_by,omitempty"`
	InvalidationReason  string     `json:"invalidation_reason,omitempty"`
	Tags                []string   `json:"tags"`
	ChangedAt           time.Time  `json:"changed_at"`
	ChangedBy           string     `json:"changed_by,omitempty"`
	ChangedByRole       string     `json:"changed_by_role,omitempty"`
	ChangeSummary       string     `json:"change_summary,omitempty"`
}

// CreateEntry inserts a new entry plus its initial history snapshot (v1).
// Returns the assigned ID.
func (s *Store) CreateEntry(ctx context.Context, e *Entry) (string, error) {
	if e == nil {
		return "", ErrInvalidInput
	}
	if e.ProjectID == "" || e.Title == "" || e.Body == "" {
		return "", fmt.Errorf("%w: project_id, title, body are required", ErrInvalidInput)
	}
	if !ValidEntryType(e.Type) {
		return "", fmt.Errorf("%w: invalid type %q", ErrInvalidInput, e.Type)
	}
	if e.Status == "" {
		e.Status = string(StatusDraft)
	}
	if !ValidStatus(e.Status) {
		return "", fmt.Errorf("%w: invalid status %q", ErrInvalidInput, e.Status)
	}
	if e.BodyFormat == "" {
		e.BodyFormat = "markdown"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	// Project must exist.
	var exists string
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM projects WHERE id = ?`, e.ProjectID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("%w: project %q does not exist", ErrInvalidInput, e.ProjectID)
		}
		return "", err
	}

	id := e.ID
	if id == "" {
		for i := 0; i < 5; i++ {
			id, err = newEntryID(e.Type)
			if err != nil {
				return "", err
			}
			var n int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM entries WHERE id = ?`, id).Scan(&n)
			if err == sql.ErrNoRows {
				break
			}
			if err != nil {
				return "", err
			}
			id = ""
		}
		if id == "" {
			return "", fmt.Errorf("failed to allocate unique entry id after retries")
		}
	}

	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO entries(
			id, project_id, type, title, status,
			symptom, root_cause, resolution, prohibited,
			attempted_approaches, observed_behavior, hypotheses,
			body, body_format, scope, metadata,
			valid_from, valid_to, superseded_by, invalidation_reason,
			enrichment_version, enrichment_at,
			created_at, updated_at, created_by, created_by_role,
			version
		) VALUES (?,?,?,?,?, ?,?,?,?, ?,?,?, ?,?,?,?, ?,?,?,?, ?,?, ?,?,?,?, 1)`,
		id, e.ProjectID, e.Type, e.Title, e.Status,
		nullable(e.Symptom), nullable(e.RootCause), nullable(e.Resolution), nullable(e.Prohibited),
		nullable(e.AttemptedApproaches), nullable(e.ObservedBehavior), nullable(e.Hypotheses),
		e.Body, e.BodyFormat, nullableRaw(e.Scope), nullableRaw(e.Metadata),
		now, nullableTime(e.ValidTo), nullable(e.SupersededBy), nullable(e.InvalidationReason),
		e.EnrichmentVersion, nullableTime(e.EnrichmentAt),
		now, now, nullable(e.CreatedBy), nullable(e.CreatedByRole),
	)
	if err != nil {
		return "", translateErr(err)
	}

	tags := normaliseTags(e.Tags)
	if err := replaceTagsTx(ctx, tx, id, tags, sourceFromRole(e.CreatedByRole)); err != nil {
		return "", err
	}

	// Initial history snapshot v1.
	if err := writeHistoryTx(ctx, tx, id, 1, &Entry{
		Title:               e.Title,
		Status:              e.Status,
		Symptom:             e.Symptom,
		RootCause:           e.RootCause,
		Resolution:          e.Resolution,
		Prohibited:          e.Prohibited,
		AttemptedApproaches: e.AttemptedApproaches,
		ObservedBehavior:    e.ObservedBehavior,
		Hypotheses:          e.Hypotheses,
		Body:                e.Body,
		BodyFormat:          e.BodyFormat,
		Scope:               e.Scope,
		Metadata:            e.Metadata,
		ValidFrom:           now,
		ValidTo:             e.ValidTo,
		SupersededBy:        e.SupersededBy,
		InvalidationReason:  e.InvalidationReason,
	}, tags, now, e.CreatedBy, e.CreatedByRole, "initial create"); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	e.ID = id
	e.CreatedAt = now
	e.UpdatedAt = now
	e.ValidFrom = now
	e.Version = 1
	return id, nil
}

// EntriesExist returns a map of {id → true} for every id in `ids` that
// has a row in `entries` (regardless of status). Missing ids are
// absent from the map (not present with value=false), so the caller's
// natural `map[id]` check returns the zero value `false`.
//
// Bulk check used by renderers that need to decide whether `[[L-XXX]]`
// references should become live links or muted "broken reference"
// indicators. One SQL round-trip regardless of len(ids).
func (s *Store) EntriesExist(ctx context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM entries WHERE id IN (`+strings.Join(placeholders, ",")+`)`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// GetEntry returns the current state.
func (s *Store) GetEntry(ctx context.Context, id string) (*Entry, error) {
	e, err := scanEntryRow(s.db.QueryRowContext(ctx, entrySelectSQL+` WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	tags, err := s.getEntryTags(ctx, id)
	if err != nil {
		return nil, err
	}
	e.Tags = tags
	return e, nil
}

// GetEntryAsOf reconstructs the entry as of the given timestamp by looking up
// the latest history snapshot with changed_at <= asOf. Immutable fields
// (id, project_id, type, created_at, created_by_role) are read from the
// current entries row. If the entry didn't exist yet at asOf, returns
// ErrNotFound.
func (s *Store) GetEntryAsOf(ctx context.Context, id string, asOf time.Time) (*Entry, error) {
	// Immutable fields from the current row.
	var (
		projectID, typ, createdBy, createdByRole string
		createdAt                                time.Time
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT project_id, type, created_at, COALESCE(created_by,''), COALESCE(created_by_role,'')
		FROM entries WHERE id = ?`, id,
	).Scan(&projectID, &typ, &createdAt, &createdBy, &createdByRole)
	if err != nil {
		return nil, translateErr(err)
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT version, title, status,
		       COALESCE(symptom,''), COALESCE(root_cause,''), COALESCE(resolution,''),
		       COALESCE(prohibited,''),
		       COALESCE(attempted_approaches,''), COALESCE(observed_behavior,''),
		       COALESCE(hypotheses,''),
		       body, body_format,
		       COALESCE(scope,''), COALESCE(metadata,''),
		       valid_from, valid_to,
		       COALESCE(superseded_by,''), COALESCE(invalidation_reason,''),
		       COALESCE(tags_snapshot,''),
		       changed_at
		FROM entry_history
		WHERE entry_id = ? AND changed_at <= ?
		ORDER BY version DESC LIMIT 1`, id, asOf)

	var (
		h        EntryHistory
		validTo  sql.NullTime
		tagsBlob string
		// EntryHistory.Scope/Metadata are json.RawMessage; same
		// empty-text-to-nil normalisation as scanEntry to avoid
		// emitting zero-byte RawMessage values that break encoding.
		scopeRaw string
		metaRaw  string
	)
	if err := row.Scan(&h.Version, &h.Title, &h.Status,
		&h.Symptom, &h.RootCause, &h.Resolution, &h.Prohibited,
		&h.AttemptedApproaches, &h.ObservedBehavior, &h.Hypotheses,
		&h.Body, &h.BodyFormat, &scopeRaw, &metaRaw,
		&h.ValidFrom, &validTo, &h.SupersededBy, &h.InvalidationReason,
		&tagsBlob, &h.ChangedAt); err != nil {
		if err == sql.ErrNoRows {
			// Entry exists today but didn't yet at asOf.
			return nil, ErrNotFound
		}
		return nil, err
	}
	if scopeRaw != "" {
		h.Scope = json.RawMessage(scopeRaw)
	}
	if metaRaw != "" {
		h.Metadata = json.RawMessage(metaRaw)
	}
	if validTo.Valid {
		t := validTo.Time
		h.ValidTo = &t
	}

	e := &Entry{
		ID:                  id,
		ProjectID:           projectID,
		Type:                typ,
		Title:               h.Title,
		Status:              h.Status,
		Symptom:             h.Symptom,
		RootCause:           h.RootCause,
		Resolution:          h.Resolution,
		Prohibited:          h.Prohibited,
		AttemptedApproaches: h.AttemptedApproaches,
		ObservedBehavior:    h.ObservedBehavior,
		Hypotheses:          h.Hypotheses,
		Body:                h.Body,
		BodyFormat:          h.BodyFormat,
		Scope:               h.Scope,
		Metadata:            h.Metadata,
		ValidFrom:           h.ValidFrom,
		ValidTo:             h.ValidTo,
		SupersededBy:        h.SupersededBy,
		InvalidationReason:  h.InvalidationReason,
		CreatedAt:           createdAt,
		UpdatedAt:           h.ChangedAt,
		CreatedBy:           createdBy,
		CreatedByRole:       createdByRole,
		Version:             h.Version,
		Tags:                decodeTagsSnapshot(tagsBlob),
	}
	return e, nil
}

// ListEntries returns (entries, total) for pagination. Total ignores
// limit/offset and counts the full filter result.
func (s *Store) ListEntries(ctx context.Context, f EntryFilter) ([]*Entry, int, error) {
	conds, args, joinTag := buildListConditions(f)

	// Count (no limit/offset).
	countSQL := "SELECT COUNT(*) FROM entries e" + joinTag
	if len(conds) > 0 {
		countSQL += " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	// Clamp explicitly: cap at the upper bound rather than
	// silently dropping to the default on overflow.
	if limit <= 0 {
		limit = 50
	} else if limit > 500 {
		limit = 500
	}
	q := entrySelectSQL + joinTag
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	if f.OldestFirst {
		q += " ORDER BY e.updated_at ASC LIMIT ? OFFSET ?"
	} else {
		q += " ORDER BY e.updated_at DESC LIMIT ? OFFSET ?"
	}
	args2 := append(append([]any{}, args...), limit, f.Offset)

	rows, err := s.db.QueryContext(ctx, q, args2...)
	if err != nil {
		return nil, 0, err
	}
	entries, err := mapRows[Entry](rows, func(c rowScanner, e *Entry) error {
		got, err := scanEntry(c)
		if err != nil {
			return err
		}
		*e = *got
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]*Entry, len(entries))
	ids := make([]string, len(entries))
	for i := range entries {
		out[i] = &entries[i]
		ids[i] = entries[i].ID
	}
	if err := s.attachTags(ctx, out, ids); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// UpdateEntry applies the patch atomically with OCC. If
// p.ExpectedVersion > 0 it must match the current version; otherwise we
// return ErrVersionMismatch (the API layer maps this to HTTP 409).
//
// Returns the new version number and the fully reconstructed Entry.
func (s *Store) UpdateEntry(ctx context.Context, id string, p EntryPatch) (int, *Entry, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()

	// Load current state inside the tx.
	cur, err := loadEntryTx(ctx, tx, id)
	if err != nil {
		return 0, nil, err
	}

	if p.ExpectedVersion > 0 && p.ExpectedVersion != cur.Version {
		return 0, nil, fmt.Errorf("%w: current=%d expected=%d",
			ErrVersionMismatch, cur.Version, p.ExpectedVersion)
	}

	// Apply patch onto a copy.
	merged := *cur
	if p.Title != nil {
		if *p.Title == "" {
			return 0, nil, fmt.Errorf("%w: title cannot be empty", ErrInvalidInput)
		}
		merged.Title = *p.Title
	}
	if p.Status != nil {
		if !ValidStatus(*p.Status) {
			return 0, nil, fmt.Errorf("%w: invalid status %q", ErrInvalidInput, *p.Status)
		}
		merged.Status = *p.Status
	}
	if p.Symptom != nil {
		merged.Symptom = *p.Symptom
	}
	if p.RootCause != nil {
		merged.RootCause = *p.RootCause
	}
	if p.Resolution != nil {
		merged.Resolution = *p.Resolution
	}
	if p.Prohibited != nil {
		merged.Prohibited = *p.Prohibited
	}
	if p.AttemptedApproaches != nil {
		merged.AttemptedApproaches = *p.AttemptedApproaches
	}
	if p.ObservedBehavior != nil {
		merged.ObservedBehavior = *p.ObservedBehavior
	}
	if p.Hypotheses != nil {
		merged.Hypotheses = *p.Hypotheses
	}
	if p.Body != nil {
		if *p.Body == "" {
			return 0, nil, fmt.Errorf("%w: body cannot be empty", ErrInvalidInput)
		}
		merged.Body = *p.Body
	}
	if p.BodyFormat != nil {
		merged.BodyFormat = *p.BodyFormat
	}
	if p.Scope != nil {
		merged.Scope = *p.Scope
	}
	if p.Metadata != nil {
		merged.Metadata = *p.Metadata
	}

	now := time.Now().UTC()
	newVersion := cur.Version + 1
	merged.Version = newVersion
	merged.UpdatedAt = now

	_, err = tx.ExecContext(ctx, `
		UPDATE entries SET
		  title = ?, status = ?,
		  symptom = ?, root_cause = ?, resolution = ?, prohibited = ?,
		  attempted_approaches = ?, observed_behavior = ?, hypotheses = ?,
		  body = ?, body_format = ?,
		  scope = ?, metadata = ?,
		  updated_at = ?, version = ?
		WHERE id = ? AND version = ?`,
		merged.Title, merged.Status,
		nullable(merged.Symptom), nullable(merged.RootCause), nullable(merged.Resolution), nullable(merged.Prohibited),
		nullable(merged.AttemptedApproaches), nullable(merged.ObservedBehavior), nullable(merged.Hypotheses),
		merged.Body, merged.BodyFormat,
		nullableRaw(merged.Scope), nullableRaw(merged.Metadata),
		now, newVersion,
		id, cur.Version,
	)
	if err != nil {
		return 0, nil, translateErr(err)
	}

	// Tags
	var tagsForSnapshot []string
	if p.Tags != nil {
		tagsForSnapshot = normaliseTags(*p.Tags)
		if err := replaceTagsTx(ctx, tx, id, tagsForSnapshot, sourceFromRole(p.ChangedByRole)); err != nil {
			return 0, nil, err
		}
	} else {
		// Snapshot the current tag set via the shared helper.
		got, err := loadTagsTx(ctx, tx, id)
		if err != nil {
			return 0, nil, err
		}
		tagsForSnapshot = got
	}
	merged.Tags = tagsForSnapshot

	if err := writeHistoryTx(ctx, tx, id, newVersion, &merged, tagsForSnapshot,
		now, p.ChangedBy, p.ChangedByRole, p.ChangeSummary); err != nil {
		return 0, nil, err
	}

	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return newVersion, &merged, nil
}

// SoftDeleteEntry marks the entry archived and sets valid_to=NOW. Idempotent.
// Bumps version and writes a history row so as-of queries see the archived
// state.
func (s *Store) SoftDeleteEntry(ctx context.Context, id, changedBy, changedByRole string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	cur, err := loadEntryTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if cur.Status == string(StatusArchived) {
		// Already archived — idempotent success.
		return tx.Commit()
	}
	now := time.Now().UTC()
	newVersion := cur.Version + 1
	cur.Status = string(StatusArchived)
	cur.ValidTo = &now
	if cur.InvalidationReason == "" {
		cur.InvalidationReason = "soft delete"
	}
	cur.Version = newVersion
	cur.UpdatedAt = now

	if _, err := tx.ExecContext(ctx, `
		UPDATE entries
		SET status = 'ARCHIVED', valid_to = ?, invalidation_reason = ?,
		    updated_at = ?, version = ?
		WHERE id = ?`,
		now, cur.InvalidationReason, now, newVersion, id); err != nil {
		return translateErr(err)
	}

	tags, err := loadTagsTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := writeHistoryTx(ctx, tx, id, newVersion, cur, tags, now,
		changedBy, changedByRole, "soft delete (ARCHIVED)"); err != nil {
		return err
	}
	return tx.Commit()
}

// EntryHistory returns all snapshots for an entry, newest version first.
func (s *Store) EntryHistory(ctx context.Context, id string) ([]*EntryHistory, error) {
	// Check existence first to return a clean 404.
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM entries WHERE id = ?`, id).Scan(&n); err != nil {
		return nil, translateErr(err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT entry_id, version, title, status,
		       COALESCE(symptom,''), COALESCE(root_cause,''), COALESCE(resolution,''),
		       COALESCE(prohibited,''),
		       COALESCE(attempted_approaches,''), COALESCE(observed_behavior,''),
		       COALESCE(hypotheses,''),
		       body, body_format,
		       COALESCE(scope,''), COALESCE(metadata,''),
		       valid_from, valid_to,
		       COALESCE(superseded_by,''), COALESCE(invalidation_reason,''),
		       COALESCE(tags_snapshot,''),
		       changed_at, COALESCE(changed_by,''), COALESCE(changed_by_role,''),
		       COALESCE(change_summary,'')
		FROM entry_history
		WHERE entry_id = ?
		ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[EntryHistory](rows, func(c rowScanner, h *EntryHistory) error {
		var (
			validTo  sql.NullTime
			tagsBlob string
			// Empty-string → nil RawMessage normalisation: see scanEntry.
			scopeRaw string
			metaRaw  string
		)
		if err := c.Scan(&h.EntryID, &h.Version, &h.Title, &h.Status,
			&h.Symptom, &h.RootCause, &h.Resolution, &h.Prohibited,
			&h.AttemptedApproaches, &h.ObservedBehavior, &h.Hypotheses,
			&h.Body, &h.BodyFormat, &scopeRaw, &metaRaw,
			&h.ValidFrom, &validTo, &h.SupersededBy, &h.InvalidationReason,
			&tagsBlob,
			&h.ChangedAt, &h.ChangedBy, &h.ChangedByRole, &h.ChangeSummary,
		); err != nil {
			return err
		}
		if scopeRaw != "" {
			h.Scope = json.RawMessage(scopeRaw)
		}
		if metaRaw != "" {
			h.Metadata = json.RawMessage(metaRaw)
		}
		if validTo.Valid {
			t := validTo.Time
			h.ValidTo = &t
		}
		h.Tags = decodeTagsSnapshot(tagsBlob)
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*EntryHistory, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// ReplaceTags is used by the enrichment writer to set tags with a specific
// source (llm/heuristic). It does not bump the entry's version.
func (s *Store) ReplaceTags(ctx context.Context, id string, tags []string, source string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replaceTagsTx(ctx, tx, id, normaliseTags(tags), source); err != nil {
		return err
	}
	return tx.Commit()
}

// SetEnrichment marks an entry as enriched at the given version. Does NOT
// bump the entry's `version` column (that is reserved for OCC of body
// changes).
func (s *Store) SetEnrichment(ctx context.Context, id string, version int) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE entries SET enrichment_version = ?, enrichment_at = CURRENT_TIMESTAMP WHERE id = ?`,
		version, id)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ===== helpers =====

const entrySelectSQL = `SELECT
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
	e.version
FROM entries e`

type scanner interface {
	Scan(dest ...any) error
}

func scanEntry(r scanner) (*Entry, error) {
	var (
		e            Entry
		validTo      sql.NullTime
		enrichmentAt sql.NullTime
		// Scope and Metadata live in TEXT columns COALESCE'd to '' for
		// NULL. We scan into a string and post-process to RawMessage:
		// empty TEXT → nil RawMessage so `omitempty` drops the field
		// from API responses cleanly. (Empty json.RawMessage that is
		// non-nil marshals as zero bytes, which breaks the surrounding
		// JSON encoding.)
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
		&e.Version)
	if err != nil {
		return nil, translateErr(err)
	}
	if scopeRaw != "" {
		e.Scope = json.RawMessage(scopeRaw)
	}
	if metaRaw != "" {
		e.Metadata = json.RawMessage(metaRaw)
	}
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
	row := tx.QueryRowContext(ctx, entrySelectSQL+` WHERE id = ?`, id)
	return scanEntry(row)
}

func buildListConditions(f EntryFilter) (conds []string, args []any, joinTag string) {
	if f.ProjectID != "" {
		conds = append(conds, "e.project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Type != "" {
		conds = append(conds, "e.type = ?")
		args = append(args, f.Type)
	}
	if f.Status != "" {
		conds = append(conds, "e.status = ?")
		args = append(args, f.Status)
	}
	if !f.IncludeSuperseded {
		conds = append(conds, "e.status NOT IN ('SUPERSEDED','ARCHIVED','DUPLICATE')")
	}
	if f.Query != "" {
		conds = append(conds, "(e.title LIKE ? OR e.body LIKE ?)")
		q := "%" + f.Query + "%"
		args = append(args, q, q)
	}
	if f.Tag != "" {
		joinTag = " JOIN tags t ON t.entry_id = e.id "
		conds = append(conds, "t.tag = ?")
		args = append(args, f.Tag)
	}
	if f.Uncategorized {
		conds = append(conds,
			"NOT EXISTS (SELECT 1 FROM use_case_entries uce WHERE uce.entry_id = e.id)")
	}
	return
}

func (s *Store) getEntryTags(ctx context.Context, id string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tag FROM tags WHERE entry_id = ? ORDER BY tag`, id)
	if err != nil {
		return nil, err
	}
	return collectStrings(rows)
}

func loadTagsTx(ctx context.Context, tx *sql.Tx, id string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT tag FROM tags WHERE entry_id = ? ORDER BY tag`, id)
	if err != nil {
		return nil, err
	}
	return collectStrings(rows)
}

func (s *Store) attachTags(ctx context.Context, entries []*Entry, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT entry_id, tag FROM tags WHERE entry_id IN (`+placeholders+`) ORDER BY entry_id, tag`,
		args...)
	if err != nil {
		return err
	}
	pairs, err := collectPairs(rows)
	if err != nil {
		return err
	}
	byID := map[string][]string{}
	for _, p := range pairs {
		byID[p.First] = append(byID[p.First], p.Second)
	}
	for _, e := range entries {
		e.Tags = byID[e.ID]
	}
	return nil
}

func replaceTagsTx(ctx context.Context, tx *sql.Tx, id string, tags []string, source string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE entry_id = ?`, id); err != nil {
		return translateErr(err)
	}
	if len(tags) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO tags(entry_id, tag, source) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, t := range tags {
		if _, err := stmt.ExecContext(ctx, id, t, source); err != nil {
			return translateErr(err)
		}
	}
	return nil
}

// normaliseTags lowercases, trims, deduplicates, and caps at 20 (design §12.5).
func normaliseTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) >= 20 {
			break
		}
	}
	sort.Strings(out)
	return out
}

// sourceFromRole picks the canonical tags.source value based on who is
// writing. Human/CLI/test writes are 'human'; agent writes are 'agent';
// librarian writes are 'librarian'. The enrichment pipeline writes via
// ReplaceTags with its own source string.
func sourceFromRole(role string) string {
	switch {
	case role == "", strings.HasPrefix(role, "human"), strings.HasPrefix(role, "token:"):
		return "human"
	case strings.HasPrefix(role, "librarian"):
		return "librarian"
	default:
		return "agent"
	}
}

func nullableTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return sql.NullTime{}
	}
	return *t
}

// writeHistoryTx records a snapshot in entry_history. The tags slice is
// serialised as a ';'-joined string (no tag contains ';' after normalisation).
func writeHistoryTx(ctx context.Context, tx *sql.Tx, id string, version int, e *Entry, tags []string,
	changedAt time.Time, changedBy, changedByRole, summary string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO entry_history(
			entry_id, version,
			title, status,
			symptom, root_cause, resolution, prohibited,
			attempted_approaches, observed_behavior, hypotheses,
			body, body_format, scope, metadata,
			valid_from, valid_to, superseded_by, invalidation_reason,
			tags_snapshot,
			changed_at, changed_by, changed_by_role, change_summary
		) VALUES (?,?, ?,?, ?,?,?,?, ?,?,?, ?,?,?,?, ?,?,?,?, ?, ?,?,?,?)`,
		id, version,
		e.Title, e.Status,
		nullable(e.Symptom), nullable(e.RootCause), nullable(e.Resolution), nullable(e.Prohibited),
		nullable(e.AttemptedApproaches), nullable(e.ObservedBehavior), nullable(e.Hypotheses),
		e.Body, e.BodyFormat, nullableRaw(e.Scope), nullableRaw(e.Metadata),
		e.ValidFrom, nullableTime(e.ValidTo), nullable(e.SupersededBy), nullable(e.InvalidationReason),
		encodeTagsSnapshot(tags),
		changedAt, nullable(changedBy), nullable(changedByRole), nullable(summary),
	)
	return translateErr(err)
}

func encodeTagsSnapshot(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return strings.Join(tags, ";")
}

func decodeTagsSnapshot(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ";")
}

// EntrySummary returns the cataloger's summary entry for `entryID`, if one
// exists. A summary is a librarian_meta entry with metadata.kind=cataloger_summary
// and metadata.source_entry_id matching the target.
//
// Phase 5 librarians write summaries as DRAFT (proposals), so we accept
// DRAFT / ACTIVE / INVESTIGATING — everything except SUPERSEDED / ARCHIVED /
// DUPLICATE. Otherwise no live summaries would ever be visible.
//
// Returns ErrNotFound when no cataloger summary has been written for this
// entry yet (the indexer / dashboard then falls back to the entry itself).
func (s *Store) EntrySummary(ctx context.Context, entryID string) (*Entry, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM entries
		 WHERE type = 'librarian_meta'
		   AND status NOT IN ('SUPERSEDED','ARCHIVED','DUPLICATE')
		   AND json_extract(metadata, '$.kind') = 'cataloger_summary'
		   AND json_extract(metadata, '$.source_entry_id') = ?
		 ORDER BY created_at DESC
		 LIMIT 1`, entryID).Scan(&id)
	if err != nil {
		return nil, translateErr(err)
	}
	return s.GetEntry(ctx, id)
}
