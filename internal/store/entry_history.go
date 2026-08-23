package store

// entry_history.go owns the entry_history snapshot table: the
// EntryHistory row type, the as-of reconstruction read (a hybrid that
// also reads immutable fields from `entries`), the history listing, and
// the snapshot writer shared by every entry mutation.

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// EntryHistory mirrors one row of entry_history (a full snapshot of all
// mutable fields at the time of a write).
type EntryHistory struct {
	EntryID             string `json:"entry_id"`
	Version             int    `json:"version"`
	Title               string `json:"title"`
	Status              string `json:"status"`
	Symptom             string `json:"symptom,omitempty"`
	RootCause           string `json:"root_cause,omitempty"`
	Resolution          string `json:"resolution,omitempty"`
	Prohibited          string `json:"prohibited,omitempty"`
	AttemptedApproaches string `json:"attempted_approaches,omitempty"`
	ObservedBehavior    string `json:"observed_behavior,omitempty"`
	Hypotheses          string `json:"hypotheses,omitempty"`
	Body                string `json:"body"`
	BodyFormat          string `json:"body_format"`
	// See Entry.Scope/Metadata for rationale — raw JSON, not strings.
	Scope              json.RawMessage `json:"scope,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	ValidFrom          time.Time       `json:"valid_from"`
	ValidTo            *time.Time      `json:"valid_to,omitempty"`
	SupersededBy       string          `json:"superseded_by,omitempty"`
	InvalidationReason string          `json:"invalidation_reason,omitempty"`
	Tags               []string        `json:"tags"`
	ChangedAt          time.Time       `json:"changed_at"`
	ChangedBy          string          `json:"changed_by,omitempty"`
	ChangedByRole      string          `json:"changed_by_role,omitempty"`
	ChangeSummary      string          `json:"change_summary,omitempty"`
}

// GetEntryAsOf reconstructs the entry as of the given timestamp by looking up
// the latest history snapshot with changed_at <= asOf. Immutable fields
// (id, project_id, type, created_at, created_by_role) are read from the
// current entries row. If the entry didn't exist yet at asOf, returns
// ErrNotFound.
func (s *Store) GetEntryAsOf(ctx context.Context, id string, asOf time.Time) (*Entry, error) {
	// Immutable fields from the current row (space-narrowed: a hidden
	// entry's history is as invisible as the entry itself).
	var (
		projectID, typ, createdBy, createdByRole, spaceID string
		createdAt                                         time.Time
	)
	q := `
		SELECT e.project_id, e.type, e.created_at, COALESCE(e.created_by,''),
		       COALESCE(e.created_by_role,''), e.space_id
		FROM entries e WHERE e.id = ?`
	args := []any{id}
	if cond, condArgs := spaceCond(ctx, "e"); cond != "" {
		q += " AND " + cond
		args = append(args, condArgs...)
	}
	err := s.db.QueryRowContext(ctx, q, args...,
	).Scan(&projectID, &typ, &createdAt, &createdBy, &createdByRole, &spaceID)
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
		// EntryHistory.Scope/Metadata are json.RawMessage; the shared
		// rawOrNil normalisation (entry_scan.go) avoids emitting
		// zero-byte RawMessage values that break encoding.
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
	h.Scope = rawOrNil(scopeRaw)
	h.Metadata = rawOrNil(metaRaw)
	if validTo.Valid {
		t := validTo.Time
		h.ValidTo = &t
	}

	e := &Entry{
		ID:                  id,
		ProjectID:           projectID,
		SpaceID:             spaceID,
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

// EntryHistory returns all snapshots for an entry, newest version first.
func (s *Store) EntryHistory(ctx context.Context, id string) ([]*EntryHistory, error) {
	// Existence + visibility gate: clean 404 for missing AND hidden.
	if err := requireVisibleEntry(ctx, s.db, id); err != nil {
		return nil, err
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
