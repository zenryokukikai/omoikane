package store

// librarian_chat.go owns the shared chat room: chat_threads +
// librarian_chat rows, plus the `@<role>` mention parsing that exists
// only for chat messages.

import (
	"context"
	"database/sql"
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
	// CreatedBy is the users.id that opened the thread (auth-context
	// authority, like ChatMessage.AuthorUserID). Empty for legacy
	// librarian threads (pre-migration 024).
	CreatedBy string `json:"created_by,omitempty"`
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

// requireVisibleRelatedEntries validates a related_entries payload — a
// JSON array of entry ids (the skill.md contract) — before it is stored
// on a thread or message. Every referenced entry must exist AND be
// visible under ctx: an invisible entry is indistinguishable from a
// missing one (ErrNotFound → uniform 404), the same contract
// CreateEntry applies to space_id. Without this check the field was an
// unvalidated reference channel (issue #103): an outsider could store a
// hidden entry's id on their own thread and any elevated-visibility
// consumer of the linkage would resolve it on their behalf. Empty means
// "no linkage"; anything unparseable is rejected rather than stored as
// free bytes that dodge the reference check.
func (s *Store) requireVisibleRelatedEntries(ctx context.Context, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return fmt.Errorf("%w: related_entries must be a JSON array of entry ids", ErrInvalidInput)
	}
	for _, id := range ids {
		if err := requireVisibleEntry(ctx, s.db, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) OpenThread(ctx context.Context, t *ChatThread) (string, error) {
	if err := s.requireVisibleRelatedEntries(ctx, t.RelatedEntries); err != nil {
		return "", err
	}
	if t.ThreadID == "" {
		t.ThreadID = newLibrarianID("thread")
	}
	if t.Status == "" {
		t.Status = "OPEN"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO chat_threads(thread_id, title, intent, status, summary, related_entries, metadata, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ThreadID, nullable(t.Title), nullable(t.Intent), t.Status,
		nullable(t.Summary), nullable(t.RelatedEntries), nullable(t.Metadata),
		nullable(t.CreatedBy))
	if err != nil {
		return "", translateErr(err)
	}
	return t.ThreadID, nil
}

// GetThread fetches one thread by id.
func (s *Store) GetThread(ctx context.Context, threadID string) (*ChatThread, error) {
	var t ChatThread
	var closed nullTimeBox
	err := s.db.QueryRowContext(ctx, `
		SELECT thread_id, COALESCE(title,''), COALESCE(intent,''), status,
		       opened_at, closed_at, COALESCE(summary,''), COALESCE(related_entries,''),
		       COALESCE(metadata,''), COALESCE(created_by,'')
		  FROM chat_threads WHERE thread_id = ?`, threadID).
		Scan(&t.ThreadID, &t.Title, &t.Intent, &t.Status, &t.OpenedAt, &closed,
			&t.Summary, &t.RelatedEntries, &t.Metadata, &t.CreatedBy)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if closed.Valid {
		x := closed.Time
		t.ClosedAt = &x
	}
	return &t, nil
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

func (s *Store) ListThreads(ctx context.Context, status, createdBy string, limit int) ([]*ChatThread, error) {
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
	sb.WriteString(`SELECT thread_id, COALESCE(title,''), COALESCE(intent,''), status,
		opened_at, closed_at, COALESCE(summary,''), COALESCE(related_entries,''),
		COALESCE(metadata,''), COALESCE(created_by,'')
		FROM chat_threads WHERE 1=1`)
	if status != "" {
		sb.WriteString(` AND status = ?`)
		args = append(args, status)
	}
	if createdBy != "" {
		sb.WriteString(` AND created_by = ?`)
		args = append(args, createdBy)
	}
	// intent=talk threads are personal conversations: a restricted view
	// (slice 4) sees only its own. Unrestricted contexts (admin scope,
	// dashboard pre-slice-5, internal jobs) are unchanged.
	if cond, condArgs := talkThreadCond(ctx, ""); cond != "" {
		sb.WriteString(` AND ` + cond)
		args = append(args, condArgs...)
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
			&t.OpenedAt, &closed, &t.Summary, &t.RelatedEntries, &t.Metadata,
			&t.CreatedBy); err != nil {
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
	// Same contract as OpenThread: every related_entries id must be a
	// visible entry (invisible == nonexistent, issue #103).
	if err := s.requireVisibleRelatedEntries(ctx, m.RelatedEntries); err != nil {
		return "", err
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
	// Clamp explicitly: cap at the upper bound rather than
	// silently dropping to the default on overflow.
	if limit <= 0 {
		limit = 200
	} else if limit > 500 {
		limit = 500
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

// chatMessageCols is the SELECT list shared by the thread-window
// queries below; keep in sync with scanChatMessage.
const chatMessageCols = `id, COALESCE(thread_id,''), timestamp, author_role,
	       COALESCE(author_instance_id,''), COALESCE(author_user_id,''),
	       COALESCE(reply_to,''), COALESCE(mentions,''), COALESCE(intent,''),
	       content, COALESCE(related_entries,''), input_tokens, output_tokens,
	       COALESCE(metadata,'')`

func scanChatMessage(c rowScanner, m *ChatMessage) error {
	return c.Scan(&m.ID, &m.ThreadID, &m.Timestamp, &m.AuthorRole,
		&m.AuthorInstanceID, &m.AuthorUserID, &m.ReplyTo, &m.Mentions,
		&m.Intent, &m.Content, &m.RelatedEntries, &m.InputTokens,
		&m.OutputTokens, &m.Metadata)
}

// listChatWindowDesc runs a newest-first window query and returns it
// oldest-first — the order the chat UI renders in.
func (s *Store) listChatWindowDesc(ctx context.Context, query string, args ...any) ([]*ChatMessage, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[ChatMessage](rows, scanChatMessage)
	if err != nil {
		return nil, err
	}
	out := make([]*ChatMessage, len(values))
	for i := range values {
		out[len(values)-1-i] = &values[i]
	}
	return out, nil
}

// ListChatMessagesTail returns the NEWEST `limit` messages of a thread,
// oldest-first. This is the initial window for the virtualized /talk
// view (#45) — the page opens at the bottom of the conversation.
func (s *Store) ListChatMessagesTail(ctx context.Context, threadID string, limit int) ([]*ChatMessage, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > 500 {
		limit = 500
	}
	return s.listChatWindowDesc(ctx, `
		SELECT `+chatMessageCols+`
		FROM librarian_chat WHERE thread_id = ?
		ORDER BY timestamp DESC LIMIT ?`, threadID, limit)
}

// ListChatMessagesBefore returns up to `limit` messages strictly older
// than beforeTS, oldest-first — the window that ends just before the
// caller's oldest rendered message (upward infinite scroll, #45).
func (s *Store) ListChatMessagesBefore(ctx context.Context, threadID string, beforeTS time.Time, limit int) ([]*ChatMessage, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > 500 {
		limit = 500
	}
	return s.listChatWindowDesc(ctx, `
		SELECT `+chatMessageCols+`
		FROM librarian_chat WHERE thread_id = ? AND timestamp < ?
		ORDER BY timestamp DESC LIMIT ?`, threadID, beforeTS, limit)
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
