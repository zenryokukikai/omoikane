package store

// librarian_work.go owns the librarians' work products: librarian_tasks,
// quartet_assignments, and external_findings + finding_correlations.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ============================================================
// librarian_tasks
// ============================================================

type LibrarianTask struct {
	TaskID      string     `json:"task_id"`
	Role        string     `json:"role"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Priority    int        `json:"priority,omitempty"`
	Status      string     `json:"status"`
	AssignedTo  string     `json:"assigned_to,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Result      string     `json:"result,omitempty"`
	Metadata    string     `json:"metadata,omitempty"`
	// SpaceID: a task minted by an open-work claim reproduces the
	// entry's title, so it lives in the entry's space (slice 4).
	// Manually enqueued tasks default to 'internal'.
	SpaceID string `json:"space_id"`
}

func (s *Store) EnqueueTask(ctx context.Context, t *LibrarianTask) (string, error) {
	if !ValidLibrarianRole(t.Role) {
		return "", fmt.Errorf("%w: invalid role %q", ErrInvalidInput, t.Role)
	}
	if t.Title == "" {
		return "", fmt.Errorf("%w: title required", ErrInvalidInput)
	}
	if t.TaskID == "" {
		t.TaskID = newLibrarianID("task")
	}
	if t.Priority == 0 {
		t.Priority = 100
	}
	if t.Status == "" {
		t.Status = "PENDING"
	}
	if t.SpaceID == "" {
		t.SpaceID = SpaceInternal
	}
	// Same contract as CreateEntry: a space the caller cannot see is
	// indistinguishable from one that does not exist.
	if err := requireVisibleSpace(ctx, s.db, t.SpaceID); err != nil {
		return "", err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO librarian_tasks(task_id, role, title, description, priority, status, assigned_to, metadata, space_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.TaskID, t.Role, t.Title, nullable(t.Description),
		t.Priority, t.Status, nullable(t.AssignedTo), nullable(t.Metadata), t.SpaceID)
	if err != nil {
		return "", translateErr(err)
	}
	return t.TaskID, nil
}

func (s *Store) ClaimTask(ctx context.Context, taskID, instanceID string) error {
	now := time.Now().UTC()
	q := `
		UPDATE librarian_tasks
		SET status='IN_PROGRESS', assigned_to=?, started_at=?
		WHERE task_id = ? AND status = 'PENDING'`
	args := []any{instanceID, now, taskID}
	// A task outside the caller's view is indistinguishable from a
	// missing one (RowsAffected 0 → ErrNotFound, mapped to 404).
	if cond, condArgs := spaceCond(ctx, ""); cond != "" {
		q += ` AND ` + cond
		args = append(args, condArgs...)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CompleteTask marks a task DONE (or FAILED if !success).
func (s *Store) CompleteTask(ctx context.Context, taskID, result string, success bool) error {
	now := time.Now().UTC()
	status := "DONE"
	if !success {
		status = "FAILED"
	}
	q := `
		UPDATE librarian_tasks
		SET status=?, completed_at=?, result=?
		WHERE task_id = ? AND status != 'DONE'`
	args := []any{status, now, nullable(result), taskID}
	if cond, condArgs := spaceCond(ctx, ""); cond != "" {
		q += ` AND ` + cond
		args = append(args, condArgs...)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListTasks(ctx context.Context, role, status string, limit int) ([]*LibrarianTask, error) {
	// Clamp explicitly: cap at the upper bound rather than
	// silently dropping to the default on overflow.
	if limit <= 0 {
		limit = 100
	} else if limit > 500 {
		limit = 500
	}
	var (
		sb   strings.Builder
		args = []any{}
	)
	sb.WriteString(`SELECT task_id, role, title, COALESCE(description,''), priority, status,
		COALESCE(assigned_to,''), created_at, started_at, completed_at,
		COALESCE(result,''), COALESCE(metadata,''), space_id
		FROM librarian_tasks WHERE 1=1`)
	if role != "" {
		sb.WriteString(` AND role = ?`)
		args = append(args, role)
	}
	if status != "" {
		sb.WriteString(` AND status = ?`)
		args = append(args, status)
	}
	// Task titles reproduce entry titles ("impl: <title>"); the list is
	// narrowed to the caller's visible spaces (slice 4).
	if cond, condArgs := spaceCond(ctx, ""); cond != "" {
		sb.WriteString(` AND ` + cond)
		args = append(args, condArgs...)
	}
	sb.WriteString(` ORDER BY priority DESC, created_at LIMIT ?`)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[LibrarianTask](rows, func(c rowScanner, t *LibrarianTask) error {
		var started, completed nullTimeBox
		if err := c.Scan(&t.TaskID, &t.Role, &t.Title, &t.Description, &t.Priority,
			&t.Status, &t.AssignedTo, &t.CreatedAt, &started, &completed,
			&t.Result, &t.Metadata, &t.SpaceID); err != nil {
			return err
		}
		if started.Valid {
			x := started.Time
			t.StartedAt = &x
		}
		if completed.Valid {
			x := completed.Time
			t.CompletedAt = &x
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*LibrarianTask, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// ============================================================
// quartet_assignments
// ============================================================

type QuartetAssignment struct {
	ID           string     `json:"id"`
	Topic        string     `json:"topic"`
	ThreadID     string     `json:"thread_id,omitempty"`
	Participant1 string     `json:"participant_1"`
	Participant2 string     `json:"participant_2"`
	Participant3 string     `json:"participant_3"`
	Judge        string     `json:"judge"`
	Status       string     `json:"status"`
	Decision     string     `json:"decision,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
	Metadata     string     `json:"metadata,omitempty"`
}

func (s *Store) CreateQuartet(ctx context.Context, q *QuartetAssignment) (string, error) {
	if q.Topic == "" {
		return "", fmt.Errorf("%w: topic required", ErrInvalidInput)
	}
	if q.Participant1 == "" || q.Participant2 == "" || q.Participant3 == "" || q.Judge == "" {
		return "", fmt.Errorf("%w: 3 participants and a judge required", ErrInvalidInput)
	}
	if q.ID == "" {
		q.ID = newLibrarianID("quartet")
	}
	if q.Status == "" {
		q.Status = "OPEN"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO quartet_assignments(id, topic, thread_id, participant_1, participant_2, participant_3, judge, status, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		q.ID, q.Topic, nullable(q.ThreadID), q.Participant1, q.Participant2, q.Participant3,
		q.Judge, q.Status, nullable(q.Metadata))
	if err != nil {
		return "", translateErr(err)
	}
	return q.ID, nil
}

func (s *Store) DecideQuartet(ctx context.Context, id, decision string) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		UPDATE quartet_assignments SET status='DECIDED', decision=?, decided_at=?
		WHERE id = ? AND status != 'DECIDED'`,
		decision, now, id)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListQuartets(ctx context.Context, status string, limit int) ([]*QuartetAssignment, error) {
	// Clamp explicitly: cap at the upper bound rather than
	// silently dropping to the default on overflow.
	if limit <= 0 {
		limit = 100
	} else if limit > 500 {
		limit = 500
	}
	var (
		sb   strings.Builder
		args = []any{}
	)
	sb.WriteString(`SELECT id, topic, COALESCE(thread_id,''), participant_1, participant_2,
		participant_3, judge, status, COALESCE(decision,''), created_at, decided_at,
		COALESCE(metadata,'')
		FROM quartet_assignments WHERE 1=1`)
	if status != "" {
		sb.WriteString(` AND status = ?`)
		args = append(args, status)
	}
	sb.WriteString(` ORDER BY created_at DESC LIMIT ?`)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[QuartetAssignment](rows, func(c rowScanner, q *QuartetAssignment) error {
		var decided nullTimeBox
		if err := c.Scan(&q.ID, &q.Topic, &q.ThreadID, &q.Participant1, &q.Participant2,
			&q.Participant3, &q.Judge, &q.Status, &q.Decision, &q.CreatedAt, &decided,
			&q.Metadata); err != nil {
			return err
		}
		if decided.Valid {
			x := decided.Time
			q.DecidedAt = &x
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*QuartetAssignment, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// ============================================================
// external_findings + finding_correlations
// ============================================================

type ExternalFinding struct {
	ID          string    `json:"id"`
	AgentLens   string    `json:"agent_lens"`
	InstanceID  string    `json:"instance_id,omitempty"`
	SourceURL   string    `json:"source_url,omitempty"`
	SourceTitle string    `json:"source_title,omitempty"`
	Excerpt     string    `json:"excerpt,omitempty"`
	Relevance   float64   `json:"relevance,omitempty"`
	Tags        string    `json:"tags,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	Metadata    string    `json:"metadata,omitempty"`
}

func (s *Store) RecordFinding(ctx context.Context, f *ExternalFinding) (string, error) {
	if f.AgentLens == "" {
		return "", fmt.Errorf("%w: agent_lens required", ErrInvalidInput)
	}
	if f.ID == "" {
		f.ID = newLibrarianID("find")
	}
	if f.Relevance == 0 {
		f.Relevance = 0.5
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO external_findings(id, agent_lens, instance_id, source_url, source_title,
		    excerpt, relevance, tags, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.AgentLens, nullable(f.InstanceID), nullable(f.SourceURL),
		nullable(f.SourceTitle), nullable(f.Excerpt), f.Relevance,
		nullable(f.Tags), nullable(f.Metadata))
	if err != nil {
		return "", translateErr(err)
	}
	return f.ID, nil
}

func (s *Store) CorrelateFinding(ctx context.Context, findingID, entryID string, correlation float64) error {
	if correlation == 0 {
		correlation = 1.0
	}
	// The entry side of the correlation must be visible (slice 3): a
	// hidden entry is indistinguishable from a missing one, and the
	// check also keeps the FK error from acting as an existence oracle.
	if err := requireVisibleEntry(ctx, s.db, entryID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO finding_correlations(finding_id, entry_id, correlation)
		VALUES (?, ?, ?)
		ON CONFLICT(finding_id, entry_id) DO UPDATE SET correlation = excluded.correlation`,
		findingID, entryID, correlation)
	return translateErr(err)
}

func (s *Store) ListFindings(ctx context.Context, agentLens string, limit int) ([]*ExternalFinding, error) {
	// Clamp explicitly: cap at the upper bound rather than
	// silently dropping to the default on overflow.
	if limit <= 0 {
		limit = 50
	} else if limit > 500 {
		limit = 500
	}
	var (
		sb   strings.Builder
		args = []any{}
	)
	sb.WriteString(`SELECT id, agent_lens, COALESCE(instance_id,''), COALESCE(source_url,''),
		COALESCE(source_title,''), COALESCE(excerpt,''), COALESCE(relevance, 0.5),
		COALESCE(tags,''), created_at, COALESCE(metadata,'')
		FROM external_findings WHERE 1=1`)
	if agentLens != "" {
		sb.WriteString(` AND agent_lens = ?`)
		args = append(args, agentLens)
	}
	sb.WriteString(` ORDER BY created_at DESC LIMIT ?`)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[ExternalFinding](rows, func(c rowScanner, f *ExternalFinding) error {
		return c.Scan(&f.ID, &f.AgentLens, &f.InstanceID, &f.SourceURL,
			&f.SourceTitle, &f.Excerpt, &f.Relevance, &f.Tags,
			&f.CreatedAt, &f.Metadata)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*ExternalFinding, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}
