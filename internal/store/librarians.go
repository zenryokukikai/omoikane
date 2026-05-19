package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// mentionRE matches `@<role>` tokens at word boundaries. The leading
// `(^|\W)` is a manual look-behind since Go's regexp engine doesn't
// support `\b` after a non-word character cleanly; this keeps
// `email@curator.com` from spuriously matching.
var mentionRE = regexp.MustCompile(
	`(^|\W)@(coordinator|cataloger|curator|detective|conservator|scout|summarizer|judge|human)\b`)

// ExtractMentions returns the list of `@<role>` tokens (deduplicated,
// insertion-ordered) referenced in `content`. Roles are limited to the
// 8 librarians + human. Plain emails / URLs containing role-shaped
// strings do not match. Returns nil (not an empty slice) when nothing
// matches, so callers can use `len(...) == 0` and reflect.DeepEqual
// both work as expected.
func ExtractMentions(content string) []string {
	matches := mentionRE.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		tag := "@" + m[2]
		if seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

// encodeMentions returns a JSON array string for storage. We always
// produce a valid JSON literal so downstream consumers can `json.Unmarshal`
// the column without a "is it the empty string?" branch.
func encodeMentions(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

// LibrarianInstance is one running (or paused) librarian.
type LibrarianInstance struct {
	InstanceID   string     `json:"instance_id"`
	Role         string     `json:"role"`
	SkillVersion string     `json:"skill_version,omitempty"`
	AgentRuntime string     `json:"agent_runtime,omitempty"`
	Status       string     `json:"status"`
	StartedAt    time.Time  `json:"started_at"`
	HeartbeatAt  *time.Time `json:"heartbeat_at,omitempty"`
	Metadata     string     `json:"metadata,omitempty"`
}

// ValidLibrarianRole reports whether r is one of the 8 canonical roles.
// Used for `librarian_instances.role` and `librarian_tasks.role` — these
// belong to specific agents, so the human author is NOT accepted here.
func ValidLibrarianRole(r string) bool {
	switch r {
	case "coordinator", "cataloger", "curator", "detective",
		"conservator", "scout", "summarizer", "judge":
		return true
	}
	return false
}

// LibrarianRoleSlice returns the canonical roles as a sorted slice.
// Used to echo the allowed list in error responses so callers can
// self-correct without a doc lookup.
func LibrarianRoleSlice() []string {
	return []string{
		"cataloger", "conservator", "coordinator", "curator",
		"detective", "judge", "scout", "summarizer",
	}
}

// ValidLibrarianRoles is a map view of the canonical roles for
// constant-time membership tests. Kept in sync with ValidLibrarianRole.
var ValidLibrarianRoles = map[string]bool{
	"coordinator": true,
	"cataloger":   true,
	"curator":     true,
	"detective":   true,
	"conservator": true,
	"scout":       true,
	"summarizer":  true,
	"judge":       true,
}

// ValidChatAuthor is the broader set of accepted `author_role` values
// on `librarian_chat`. In addition to the 8 librarians, "human" is
// allowed so the user (Z-axis observer per design.md §24) can join the
// shared chat. Phase 5 ships this so the dashboard chat room is
// actually two-way.
func ValidChatAuthor(r string) bool {
	if r == "human" {
		return true
	}
	return ValidLibrarianRole(r)
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
	ThreadID       string     `json:"thread_id"`
	Title          string     `json:"title,omitempty"`
	Intent         string     `json:"intent,omitempty"`
	Status         string     `json:"status"`
	OpenedAt       time.Time  `json:"opened_at"`
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
	Summary        string     `json:"summary,omitempty"`
	RelatedEntries string     `json:"related_entries,omitempty"`
	Metadata       string     `json:"metadata,omitempty"`
}

type ChatMessage struct {
	ID               string    `json:"id"`
	ThreadID         string    `json:"thread_id,omitempty"`
	Timestamp        time.Time `json:"timestamp"`
	AuthorRole       string    `json:"author_role"`
	AuthorInstanceID string    `json:"author_instance_id,omitempty"`
	// AuthorUserID is the users.id of whoever actually posted this
	// message — the auth-context source of truth. Empty for legacy
	// messages written before migration 012. The API layer fills this
	// in from the bearer token; clients can't set it themselves.
	AuthorUserID   string `json:"author_user_id,omitempty"`
	ReplyTo        string `json:"reply_to,omitempty"`
	Mentions       string `json:"mentions,omitempty"`
	Intent         string `json:"intent,omitempty"`
	Content        string `json:"content"`
	RelatedEntries string `json:"related_entries,omitempty"`
	InputTokens    int    `json:"input_tokens,omitempty"`
	OutputTokens   int    `json:"output_tokens,omitempty"`
	Metadata       string `json:"metadata,omitempty"`
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
	if !ValidChatAuthor(m.AuthorRole) {
		return "", fmt.Errorf("%w: invalid author_role %q", ErrInvalidInput, m.AuthorRole)
	}
	if strings.TrimSpace(m.Content) == "" {
		return "", fmt.Errorf("%w: content required", ErrInvalidInput)
	}
	if m.ID == "" {
		m.ID = newLibrarianID("msg")
	}
	// Auto-extract `@<role>` mentions from the body when the caller
	// hasn't supplied them explicitly. Honour caller-provided values
	// verbatim so a tool that wants to mention a non-textual role (or
	// suppress mentions entirely with `"[]"`) can.
	if m.Mentions == "" {
		m.Mentions = encodeMentions(ExtractMentions(m.Content))
	}
	// Explicit nanosecond-precision timestamp. The schema's DEFAULT
	// CURRENT_TIMESTAMP only has second precision, which made the
	// long-poll cursor (`WHERE timestamp > ?`) silently drop any
	// message posted in the same second as the previous one. Go's
	// time.Now().UTC() serialised by the sqlite driver preserves
	// nanoseconds, so cursors are reliable.
	if m.Timestamp.IsZero() {
		m.Timestamp = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO librarian_chat(id, thread_id, timestamp, author_role,
		    author_instance_id, author_user_id, reply_to, mentions, intent,
		    content, related_entries, input_tokens, output_tokens, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, nullable(m.ThreadID), m.Timestamp, m.AuthorRole,
		nullable(m.AuthorInstanceID), nullable(m.AuthorUserID),
		nullable(m.ReplyTo), nullable(m.Mentions), nullable(m.Intent),
		m.Content, nullable(m.RelatedEntries),
		m.InputTokens, m.OutputTokens, nullable(m.Metadata))
	if err != nil {
		return "", translateErr(err)
	}
	return m.ID, nil
}

func (s *Store) ListChatMessages(ctx context.Context, threadID string, limit int) ([]*ChatMessage, error) {
	return s.ListChatMessagesSince(ctx, threadID, time.Time{}, limit)
}

// ListChatMessagesSince returns messages newer than `sinceTS` in the
// thread, ordered by timestamp ASC. Pass a zero time to get all
// messages (same as ListChatMessages). The strict `>` comparison
// means passing the timestamp of your last-seen message reliably
// excludes that message from the new batch.
func (s *Store) ListChatMessagesSince(ctx context.Context, threadID string, sinceTS time.Time, limit int) ([]*ChatMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var rows *sql.Rows
	var err error
	if sinceTS.IsZero() {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, COALESCE(thread_id,''), timestamp, author_role,
			       COALESCE(author_instance_id,''), COALESCE(author_user_id,''),
			       COALESCE(reply_to,''), COALESCE(mentions,''), COALESCE(intent,''),
			       content, COALESCE(related_entries,''), input_tokens, output_tokens,
			       COALESCE(metadata,'')
			FROM librarian_chat WHERE thread_id = ?
			ORDER BY timestamp ASC LIMIT ?`, threadID, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, COALESCE(thread_id,''), timestamp, author_role,
			       COALESCE(author_instance_id,''), COALESCE(author_user_id,''),
			       COALESCE(reply_to,''), COALESCE(mentions,''), COALESCE(intent,''),
			       content, COALESCE(related_entries,''), input_tokens, output_tokens,
			       COALESCE(metadata,'')
			FROM librarian_chat WHERE thread_id = ? AND timestamp > ?
			ORDER BY timestamp ASC LIMIT ?`, threadID, sinceTS, limit)
	}
	if err != nil {
		return nil, err
	}
	values, err := mapRows[ChatMessage](rows, func(c rowScanner, m *ChatMessage) error {
		return c.Scan(&m.ID, &m.ThreadID, &m.Timestamp, &m.AuthorRole,
			&m.AuthorInstanceID, &m.AuthorUserID, &m.ReplyTo, &m.Mentions,
			&m.Intent, &m.Content, &m.RelatedEntries, &m.InputTokens,
			&m.OutputTokens, &m.Metadata)
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

// GetChatMessage returns one message by id. Used by the long-poll
// endpoint to resolve a client-supplied `since` message id to its
// timestamp so the cursor query can use a SARGable comparison.
func (s *Store) GetChatMessage(ctx context.Context, id string) (*ChatMessage, error) {
	var m ChatMessage
	err := s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(thread_id,''), timestamp, author_role,
		       COALESCE(author_instance_id,''), COALESCE(author_user_id,''),
		       COALESCE(reply_to,''), COALESCE(mentions,''), COALESCE(intent,''),
		       content, COALESCE(related_entries,''), input_tokens, output_tokens,
		       COALESCE(metadata,'')
		FROM librarian_chat WHERE id = ?`, id).Scan(
		&m.ID, &m.ThreadID, &m.Timestamp, &m.AuthorRole,
		&m.AuthorInstanceID, &m.AuthorUserID, &m.ReplyTo, &m.Mentions,
		&m.Intent, &m.Content, &m.RelatedEntries, &m.InputTokens,
		&m.OutputTokens, &m.Metadata)
	if err != nil {
		return nil, translateErr(err)
	}
	return &m, nil
}

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
