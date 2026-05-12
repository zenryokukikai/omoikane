package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// LibrarianInstance is one running (or paused) librarian.
type LibrarianInstance struct {
	InstanceID    string
	Role          string
	SkillVersion  string
	AgentRuntime  string
	Status        string
	StartedAt     time.Time
	HeartbeatAt   *time.Time
	Metadata      string
}

// ValidLibrarianRole reports whether r is one of the 8 canonical roles.
func ValidLibrarianRole(r string) bool {
	switch r {
	case "coordinator", "cataloger", "curator", "detective",
		"conservator", "scout", "summarizer", "judge":
		return true
	}
	return false
}

func newLibrarianID(prefix string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

func (s *Store) RegisterLibrarianInstance(ctx context.Context, i *LibrarianInstance) (string, error) {
	if !ValidLibrarianRole(i.Role) {
		return "", fmt.Errorf("%w: invalid role %q", ErrInvalidInput, i.Role)
	}
	if i.InstanceID == "" {
		i.InstanceID = i.Role + "-" + newLibrarianID("inst")[5:]
	}
	if i.Status == "" {
		i.Status = "OBSERVING"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO librarian_instances(instance_id, role, skill_version, agent_runtime, status, metadata)
		VALUES (?, ?, ?, ?, ?, ?)`,
		i.InstanceID, i.Role, nullable(i.SkillVersion),
		nullable(i.AgentRuntime), i.Status, nullable(i.Metadata))
	if err != nil {
		return "", translateErr(err)
	}
	return i.InstanceID, nil
}

func (s *Store) SetLibrarianStatus(ctx context.Context, instanceID, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE librarian_instances SET status = ? WHERE instance_id = ?`,
		status, instanceID)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RecordHeartbeat(ctx context.Context, instanceID string) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`UPDATE librarian_instances SET heartbeat_at = ? WHERE instance_id = ?`,
		now, instanceID)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListLibrarianInstances(ctx context.Context, role, status string) ([]*LibrarianInstance, error) {
	var (
		sb   strings.Builder
		args = []any{}
	)
	sb.WriteString(`SELECT instance_id, role, COALESCE(skill_version,''), COALESCE(agent_runtime,''),
		status, started_at, heartbeat_at, COALESCE(metadata,'')
		FROM librarian_instances WHERE 1=1`)
	if role != "" {
		sb.WriteString(` AND role = ?`)
		args = append(args, role)
	}
	if status != "" {
		sb.WriteString(` AND status = ?`)
		args = append(args, status)
	}
	sb.WriteString(` ORDER BY role, instance_id`)
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[LibrarianInstance](rows, func(c rowScanner, i *LibrarianInstance) error {
		var hb nullTimeBox
		if err := c.Scan(&i.InstanceID, &i.Role, &i.SkillVersion, &i.AgentRuntime,
			&i.Status, &i.StartedAt, &hb, &i.Metadata); err != nil {
			return err
		}
		if hb.Valid {
			t := hb.Time
			i.HeartbeatAt = &t
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*LibrarianInstance, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// ============================================================
// chat_threads + librarian_chat
// ============================================================

type ChatThread struct {
	ThreadID       string
	Title          string
	Intent         string
	Status         string
	OpenedAt       time.Time
	ClosedAt       *time.Time
	Summary        string
	RelatedEntries string
	Metadata       string
}

type ChatMessage struct {
	ID               string
	ThreadID         string
	Timestamp        time.Time
	AuthorRole       string
	AuthorInstanceID string
	ReplyTo          string
	Mentions         string
	Intent           string
	Content          string
	RelatedEntries   string
	InputTokens      int
	OutputTokens     int
	Metadata         string
}

func (s *Store) OpenThread(ctx context.Context, t *ChatThread) (string, error) {
	if t.ThreadID == "" {
		t.ThreadID = newLibrarianID("thread")
	}
	if t.Status == "" {
		t.Status = "OPEN"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO chat_threads(thread_id, title, intent, status, summary, related_entries, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ThreadID, nullable(t.Title), nullable(t.Intent), t.Status,
		nullable(t.Summary), nullable(t.RelatedEntries), nullable(t.Metadata))
	if err != nil {
		return "", translateErr(err)
	}
	return t.ThreadID, nil
}

func (s *Store) CloseThread(ctx context.Context, threadID, summary string) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		UPDATE chat_threads SET status='CLOSED', closed_at=?, summary=?
		WHERE thread_id = ?`, now, nullable(summary), threadID)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListThreads(ctx context.Context, status string, limit int) ([]*ChatThread, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var (
		sb   strings.Builder
		args = []any{}
	)
	sb.WriteString(`SELECT thread_id, COALESCE(title,''), COALESCE(intent,''), status,
		opened_at, closed_at, COALESCE(summary,''), COALESCE(related_entries,''),
		COALESCE(metadata,'')
		FROM chat_threads WHERE 1=1`)
	if status != "" {
		sb.WriteString(` AND status = ?`)
		args = append(args, status)
	}
	sb.WriteString(` ORDER BY opened_at DESC LIMIT ?`)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[ChatThread](rows, func(c rowScanner, t *ChatThread) error {
		var closed nullTimeBox
		if err := c.Scan(&t.ThreadID, &t.Title, &t.Intent, &t.Status,
			&t.OpenedAt, &closed, &t.Summary, &t.RelatedEntries, &t.Metadata); err != nil {
			return err
		}
		if closed.Valid {
			x := closed.Time
			t.ClosedAt = &x
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*ChatThread, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

func (s *Store) PostChatMessage(ctx context.Context, m *ChatMessage) (string, error) {
	if !ValidLibrarianRole(m.AuthorRole) {
		return "", fmt.Errorf("%w: invalid author_role %q", ErrInvalidInput, m.AuthorRole)
	}
	if strings.TrimSpace(m.Content) == "" {
		return "", fmt.Errorf("%w: content required", ErrInvalidInput)
	}
	if m.ID == "" {
		m.ID = newLibrarianID("msg")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO librarian_chat(id, thread_id, author_role, author_instance_id,
		    reply_to, mentions, intent, content, related_entries,
		    input_tokens, output_tokens, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, nullable(m.ThreadID), m.AuthorRole, nullable(m.AuthorInstanceID),
		nullable(m.ReplyTo), nullable(m.Mentions), nullable(m.Intent), m.Content,
		nullable(m.RelatedEntries), m.InputTokens, m.OutputTokens, nullable(m.Metadata))
	if err != nil {
		return "", translateErr(err)
	}
	return m.ID, nil
}

func (s *Store) ListChatMessages(ctx context.Context, threadID string, limit int) ([]*ChatMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(thread_id,''), timestamp, author_role,
		       COALESCE(author_instance_id,''), COALESCE(reply_to,''),
		       COALESCE(mentions,''), COALESCE(intent,''), content,
		       COALESCE(related_entries,''), input_tokens, output_tokens,
		       COALESCE(metadata,'')
		FROM librarian_chat WHERE thread_id = ?
		ORDER BY timestamp ASC LIMIT ?`, threadID, limit)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[ChatMessage](rows, func(c rowScanner, m *ChatMessage) error {
		return c.Scan(&m.ID, &m.ThreadID, &m.Timestamp, &m.AuthorRole,
			&m.AuthorInstanceID, &m.ReplyTo, &m.Mentions, &m.Intent, &m.Content,
			&m.RelatedEntries, &m.InputTokens, &m.OutputTokens, &m.Metadata)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*ChatMessage, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// ============================================================
// librarian_tasks
// ============================================================

type LibrarianTask struct {
	TaskID       string
	Role         string
	Title        string
	Description  string
	Priority     int
	Status       string
	AssignedTo   string
	CreatedAt    time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
	Result       string
	Metadata     string
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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO librarian_tasks(task_id, role, title, description, priority, status, assigned_to, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.TaskID, t.Role, t.Title, nullable(t.Description),
		t.Priority, t.Status, nullable(t.AssignedTo), nullable(t.Metadata))
	if err != nil {
		return "", translateErr(err)
	}
	return t.TaskID, nil
}

func (s *Store) ClaimTask(ctx context.Context, taskID, instanceID string) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		UPDATE librarian_tasks
		SET status='IN_PROGRESS', assigned_to=?, started_at=?
		WHERE task_id = ? AND status = 'PENDING'`,
		instanceID, now, taskID)
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
	res, err := s.db.ExecContext(ctx, `
		UPDATE librarian_tasks
		SET status=?, completed_at=?, result=?
		WHERE task_id = ? AND status != 'DONE'`,
		status, now, nullable(result), taskID)
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
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var (
		sb   strings.Builder
		args = []any{}
	)
	sb.WriteString(`SELECT task_id, role, title, COALESCE(description,''), priority, status,
		COALESCE(assigned_to,''), created_at, started_at, completed_at,
		COALESCE(result,''), COALESCE(metadata,'')
		FROM librarian_tasks WHERE 1=1`)
	if role != "" {
		sb.WriteString(` AND role = ?`)
		args = append(args, role)
	}
	if status != "" {
		sb.WriteString(` AND status = ?`)
		args = append(args, status)
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
			&t.Result, &t.Metadata); err != nil {
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
	ID           string
	Topic        string
	ThreadID     string
	Participant1 string
	Participant2 string
	Participant3 string
	Judge        string
	Status       string
	Decision     string
	CreatedAt    time.Time
	DecidedAt    *time.Time
	Metadata     string
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
	if limit <= 0 || limit > 500 {
		limit = 100
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
	ID          string
	AgentLens   string
	InstanceID  string
	SourceURL   string
	SourceTitle string
	Excerpt     string
	Relevance   float64
	Tags        string
	CreatedAt   time.Time
	Metadata    string
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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO finding_correlations(finding_id, entry_id, correlation)
		VALUES (?, ?, ?)
		ON CONFLICT(finding_id, entry_id) DO UPDATE SET correlation = excluded.correlation`,
		findingID, entryID, correlation)
	return translateErr(err)
}

func (s *Store) ListFindings(ctx context.Context, agentLens string, limit int) ([]*ExternalFinding, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
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
