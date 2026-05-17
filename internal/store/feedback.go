package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Feedback signal vocabulary.
//
// Six values picked to cover the actual ways an agent uses prior knowledge:
// applying it, having it confirm what they already knew, finding it
// stale/wrong/incomplete, or being reminded of a gap in their own model.
// Compare to the legacy usage_cases.result (helpful/partially_helpful/
// not_helpful/misleading/unknown) which assumed the only flow was
// "pre-flight check → action".
const (
	FeedbackSignalHelpful      = "helpful"       // applied / directly used
	FeedbackSignalConfirmed    = "confirmed"     // reinforced existing knowledge
	FeedbackSignalOutdated     = "outdated"      // factually correct in the past, no longer current
	FeedbackSignalWrong        = "wrong"         // factually incorrect
	FeedbackSignalIncomplete   = "incomplete"    // correct but missing important context
	FeedbackSignalSurfacedGap  = "surfaced_gap"  // reading revealed a gap in MY (reader's) model
)

var validFeedbackSignals = map[string]bool{
	FeedbackSignalHelpful:     true,
	FeedbackSignalConfirmed:   true,
	FeedbackSignalOutdated:    true,
	FeedbackSignalWrong:       true,
	FeedbackSignalIncomplete:  true,
	FeedbackSignalSurfacedGap: true,
}

// FeedbackSignals returns the valid vocabulary. Used by the API layer to
// echo back the allowed values on a validation error so an agent's next
// retry succeeds without an out-of-band doc lookup.
func FeedbackSignals() []string {
	return []string{
		FeedbackSignalHelpful,
		FeedbackSignalConfirmed,
		FeedbackSignalOutdated,
		FeedbackSignalWrong,
		FeedbackSignalIncomplete,
		FeedbackSignalSurfacedGap,
	}
}

// EntryFeedback is one row in entry_feedback. ID is assigned by the DB.
type EntryFeedback struct {
	ID        int64
	EntryID   string
	UserID    string
	Signal    string
	Context   string
	CreatedAt time.Time
}

// RecordFeedback inserts one explicit feedback row. The entry must exist
// (we don't permit feedback on phantoms — that would let typos accumulate
// silently). The signal must be in the validFeedbackSignals set.
//
// We do NOT enforce per-(entry, user) uniqueness. An agent revisiting an
// old entry months later may legitimately say "helpful" once and
// "outdated" later; both signals are useful and we keep the full stream.
// The entry_engagement view aggregates over all rows.
func (s *Store) RecordFeedback(ctx context.Context, fb *EntryFeedback) error {
	if fb.EntryID == "" {
		return fmt.Errorf("%w: entry_id required", ErrInvalidInput)
	}
	if !validFeedbackSignals[fb.Signal] {
		return fmt.Errorf("%w: signal must be one of %v", ErrInvalidInput, FeedbackSignals())
	}
	// Verify entry exists. We deliberately allow feedback on SUPERSEDED or
	// DELETED entries — those signals are still useful ("this used to work
	// but no longer applies" is exactly the outdated/wrong feedback we
	// want).
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM entries WHERE id = ?`, fb.EntryID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: entry %s", ErrNotFound, fb.EntryID)
	}
	if err != nil {
		return err
	}
	var u, c any
	if fb.UserID == "" {
		u = nil
	} else {
		u = fb.UserID
	}
	if fb.Context == "" {
		c = nil
	} else {
		c = fb.Context
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO entry_feedback (entry_id, user_id, signal, context)
		 VALUES (?, ?, ?, ?)`,
		fb.EntryID, u, fb.Signal, c)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	fb.ID = id
	// Populate CreatedAt for the caller. Read it back so we don't fork
	// from what SQLite recorded.
	err = s.db.QueryRowContext(ctx,
		`SELECT created_at FROM entry_feedback WHERE id = ?`, id).Scan(&fb.CreatedAt)
	if err != nil {
		// Non-fatal: insert succeeded, only the read-back failed. Leave
		// CreatedAt zero; caller can re-fetch if needed.
		return nil
	}
	return nil
}

// EntryEngagement aggregates passive access + explicit feedback signals.
// Mirrors the entry_engagement view defined in migration 016.
type EntryEngagement struct {
	EntryID             string  `json:"entry_id"`
	ProjectID           string  `json:"project_id"`
	ReferenceCount30d   int     `json:"reference_count_30d"`
	ReferenceCountTotal int     `json:"reference_count_total"`
	FeedbackHelpful     int     `json:"feedback_helpful"`
	FeedbackConfirmed   int     `json:"feedback_confirmed"`
	FeedbackOutdated    int     `json:"feedback_outdated"`
	FeedbackWrong       int     `json:"feedback_wrong"`
	FeedbackIncomplete  int     `json:"feedback_incomplete"`
	FeedbackSurfacedGap int     `json:"feedback_surfaced_gap"`
	// EngagementScore is roughly in [-1, +1] with smoothing toward 0 for
	// low-feedback entries. See migration 016 for the formula.
	EngagementScore float64 `json:"engagement_score"`
}

// GetEngagement returns the engagement view row for one entry, or
// ErrNotFound if the entry doesn't exist.
func (s *Store) GetEngagement(ctx context.Context, entryID string) (*EntryEngagement, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT entry_id, project_id, reference_count_30d, reference_count_total,
		        feedback_helpful, feedback_confirmed, feedback_outdated,
		        feedback_wrong, feedback_incomplete, feedback_surfaced_gap,
		        engagement_score
		 FROM entry_engagement WHERE entry_id = ?`, entryID)
	var e EntryEngagement
	err := row.Scan(
		&e.EntryID, &e.ProjectID, &e.ReferenceCount30d, &e.ReferenceCountTotal,
		&e.FeedbackHelpful, &e.FeedbackConfirmed, &e.FeedbackOutdated,
		&e.FeedbackWrong, &e.FeedbackIncomplete, &e.FeedbackSurfacedGap,
		&e.EngagementScore,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}
