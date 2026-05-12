package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// UsageCase is one row of usage_cases.
type UsageCase struct {
	CaseID            string
	EntryID           string
	ProjectID         string
	ClientType        string
	ClientVersion     string
	SessionID         string
	AgentRole         string
	AgentLabel        string
	RetrievedAt       time.Time
	TriggerQuery      string
	TaskContext       string
	Environment       string
	Outcome           string
	ApplicationDetail string
	RejectionReason   string
	Result            string
	ResultEvidence    string
	ResultJudgedBy    string
	ResultJudgedAt    *time.Time
	Notes             string
	Metadata          string
}

// CasePatch is the partial update payload used by PATCH /v1/cases/{id}.
type CasePatch struct {
	Outcome           *string
	ApplicationDetail *string
	RejectionReason   *string
	Result            *string
	ResultEvidence    *string
	ResultJudgedBy    *string
	Notes             *string
}

// EntrySignals mirrors a row in the entry_signals view.
type EntrySignals struct {
	ID               string
	ProjectID        string
	Title            string
	Type             string
	Status           string
	TotalUses        int
	HelpfulCount     int
	PartialCount     int
	NotHelpfulCount  int
	MisleadingCount  int
	UnknownCount     int
	LastRetrievedAt  *time.Time
	HelpfulnessScore *float64
}

// ReviewQueueRow is a row in the review_queue view.
type ReviewQueueRow struct {
	ID               string
	Title            string
	Type             string
	Status           string
	MisleadingCount  int
	TotalUses        int
	HelpfulnessScore *float64
}

// newCaseID returns a 16-char hex case identifier. We don't reuse the
// entry-style A-B base32 form because cases are far more numerous and we
// want collision-free generation without round-tripping.
func newCaseID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "CASE-" + hex.EncodeToString(b[:])
}

// CreateCase inserts a new usage case. CaseID is generated when empty.
// `entry_id` must exist or the foreign key fires.
func (s *Store) CreateCase(ctx context.Context, c *UsageCase) (string, error) {
	if c.EntryID == "" {
		return "", fmt.Errorf("%w: entry_id required", ErrInvalidInput)
	}
	if c.CaseID == "" {
		c.CaseID = newCaseID()
	}
	if c.RetrievedAt.IsZero() {
		c.RetrievedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO usage_cases(
			case_id, entry_id, project_id,
			client_type, client_version, session_id, agent_role, agent_label,
			retrieved_at, trigger_query, task_context, environment,
			outcome, application_detail, rejection_reason,
			result, result_evidence, result_judged_by, result_judged_at,
			notes, metadata
		) VALUES (?,?,?, ?,?,?,?,?, ?,?,?,?, ?,?,?, ?,?,?,?, ?,?)`,
		c.CaseID, c.EntryID, nullable(c.ProjectID),
		nullable(c.ClientType), nullable(c.ClientVersion), nullable(c.SessionID),
		nullable(c.AgentRole), nullable(c.AgentLabel),
		c.RetrievedAt, nullable(c.TriggerQuery), nullable(c.TaskContext),
		nullable(c.Environment),
		nullable(c.Outcome), nullable(c.ApplicationDetail), nullable(c.RejectionReason),
		nullable(c.Result), nullable(c.ResultEvidence),
		nullable(c.ResultJudgedBy), nullableTime(c.ResultJudgedAt),
		nullable(c.Notes), nullable(c.Metadata),
	)
	if err != nil {
		return "", translateErr(err)
	}
	return c.CaseID, nil
}

// PatchCase applies the partial update. The result_judged_at column is set
// automatically when `result` transitions from NULL/unknown to anything else.
func (s *Store) PatchCase(ctx context.Context, caseID string, p CasePatch) (*UsageCase, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var existing UsageCase
	row := tx.QueryRowContext(ctx, caseSelectSQL+` WHERE case_id = ?`, caseID)
	if err := scanCase(row, &existing); err != nil {
		return nil, translateErr(err)
	}

	sets := []string{}
	args := []any{}
	addSet := func(col string, ptr *string) {
		if ptr == nil {
			return
		}
		sets = append(sets, col+" = ?")
		args = append(args, nullable(*ptr))
	}
	addSet("outcome", p.Outcome)
	addSet("application_detail", p.ApplicationDetail)
	addSet("rejection_reason", p.RejectionReason)
	addSet("result", p.Result)
	addSet("result_evidence", p.ResultEvidence)
	addSet("result_judged_by", p.ResultJudgedBy)
	addSet("notes", p.Notes)

	// Auto-stamp result_judged_at when result transitions to a real value.
	if p.Result != nil && *p.Result != "" && *p.Result != "unknown" {
		if existing.ResultJudgedAt == nil {
			sets = append(sets, "result_judged_at = ?")
			args = append(args, time.Now().UTC())
		}
	}
	if len(sets) == 0 {
		return &existing, tx.Commit()
	}
	args = append(args, caseID)
	if _, err := tx.ExecContext(ctx,
		"UPDATE usage_cases SET "+strings.Join(sets, ", ")+" WHERE case_id = ?",
		args...,
	); err != nil {
		return nil, translateErr(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetCase(ctx, caseID)
}

func (s *Store) GetCase(ctx context.Context, caseID string) (*UsageCase, error) {
	var c UsageCase
	row := s.db.QueryRowContext(ctx, caseSelectSQL+` WHERE case_id = ?`, caseID)
	if err := scanCase(row, &c); err != nil {
		return nil, translateErr(err)
	}
	return &c, nil
}

// ListCases returns cases for an entry (newest first). Limit defaults to 50.
func (s *Store) ListCases(ctx context.Context, entryID string, limit int) ([]*UsageCase, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		caseSelectSQL+` WHERE entry_id = ? ORDER BY retrieved_at DESC LIMIT ?`,
		entryID, limit)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[UsageCase](rows, func(c rowScanner, x *UsageCase) error {
		return scanCase(c, x)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*UsageCase, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// EntrySignal returns the aggregated signals row for one entry. Returns
// zero-valued struct (no error) when the entry has no cases.
func (s *Store) EntrySignal(ctx context.Context, entryID string) (*EntrySignals, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, title, type, status,
		       total_uses, helpful_count, partial_count, not_helpful_count,
		       misleading_count, unknown_count,
		       last_retrieved_at, helpfulness_score
		FROM entry_signals WHERE id = ?`, entryID)
	var (
		es              EntrySignals
		lastRetrieved   nullTimeBox
		helpfulness     nullFloat
	)
	if err := row.Scan(&es.ID, &es.ProjectID, &es.Title, &es.Type, &es.Status,
		&es.TotalUses, &es.HelpfulCount, &es.PartialCount, &es.NotHelpfulCount,
		&es.MisleadingCount, &es.UnknownCount,
		&lastRetrieved, &helpfulness); err != nil {
		return nil, translateErr(err)
	}
	if lastRetrieved.Valid {
		t := lastRetrieved.Time
		es.LastRetrievedAt = &t
	}
	if helpfulness.Valid {
		v := helpfulness.Value
		es.HelpfulnessScore = &v
	}
	return &es, nil
}

// HelpfulnessScores fetches helpfulness scores for many entry IDs at once;
// the returned map only has entries for IDs that have at least one judged
// case. The lookup ranker uses this to bias scores toward entries proven
// useful.
func (s *Store) HelpfulnessScores(ctx context.Context, ids []string) (map[string]float64, error) {
	out := map[string]float64{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, helpfulness_score FROM entry_signals
		 WHERE id IN (`+placeholders+`) AND helpfulness_score IS NOT NULL`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var score float64
		if err := rows.Scan(&id, &score); err != nil {
			return nil, err
		}
		out[id] = score
	}
	return out, rows.Err()
}

// ReviewQueue returns rows from the review_queue view.
func (s *Store) ReviewQueue(ctx context.Context, limit int) ([]*ReviewQueueRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, type, status,
		       misleading_count, total_uses, helpfulness_score
		FROM review_queue
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[ReviewQueueRow](rows, func(c rowScanner, r *ReviewQueueRow) error {
		var score nullFloat
		if err := c.Scan(&r.ID, &r.Title, &r.Type, &r.Status,
			&r.MisleadingCount, &r.TotalUses, &score); err != nil {
			return err
		}
		if score.Valid {
			v := score.Value
			r.HelpfulnessScore = &v
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*ReviewQueueRow, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// nullFloat scans a REAL column that may be NULL.
type nullFloat struct {
	Valid bool
	Value float64
}

func (n *nullFloat) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		n.Valid = false
	case float64:
		n.Valid = true
		n.Value = v
	case int64:
		n.Valid = true
		n.Value = float64(v)
	case []byte:
		var f float64
		if _, err := fmt.Sscanf(string(v), "%f", &f); err != nil {
			return err
		}
		n.Valid = true
		n.Value = f
	case string:
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err != nil {
			return err
		}
		n.Valid = true
		n.Value = f
	}
	return nil
}

const caseSelectSQL = `SELECT
	case_id, entry_id, COALESCE(project_id, ''),
	COALESCE(client_type, ''), COALESCE(client_version, ''),
	COALESCE(session_id, ''), COALESCE(agent_role, ''), COALESCE(agent_label, ''),
	retrieved_at, COALESCE(trigger_query, ''), COALESCE(task_context, ''),
	COALESCE(environment, ''),
	COALESCE(outcome, ''), COALESCE(application_detail, ''),
	COALESCE(rejection_reason, ''),
	COALESCE(result, ''), COALESCE(result_evidence, ''),
	COALESCE(result_judged_by, ''), result_judged_at,
	COALESCE(notes, ''), COALESCE(metadata, '')
FROM usage_cases`

func scanCase(r scanOne, c *UsageCase) error {
	var judgedAt nullTimeBox
	err := r.Scan(&c.CaseID, &c.EntryID, &c.ProjectID,
		&c.ClientType, &c.ClientVersion, &c.SessionID, &c.AgentRole, &c.AgentLabel,
		&c.RetrievedAt, &c.TriggerQuery, &c.TaskContext, &c.Environment,
		&c.Outcome, &c.ApplicationDetail, &c.RejectionReason,
		&c.Result, &c.ResultEvidence, &c.ResultJudgedBy, &judgedAt,
		&c.Notes, &c.Metadata)
	if err != nil {
		return err
	}
	if judgedAt.Valid {
		t := judgedAt.Time
		c.ResultJudgedAt = &t
	}
	return nil
}
