package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// EntryComment is one review/discussion comment anchored to a single
// entry. Authored by humans AND agents alike — see design.md §23.21.
//
// The author is a users(id) FK; the display fields (AuthorName, AuthorKind,
// AuthorLibrarianRole) are filled by a JOIN to users at read time, never
// stored on the comment, so a renamed user or role change is reflected
// everywhere without a backfill.
type EntryComment struct {
	ID                 string    `json:"id"`
	EntryID            string    `json:"entry_id"`
	AuthorUserID       string    `json:"author_user_id"`
	AuthorName         string    `json:"author_name"`
	AuthorKind         string    `json:"author_kind"`            // "human" | "agent"
	AuthorLibrarianRole string   `json:"author_librarian_role,omitempty"`
	AuthorAvatarURL    string    `json:"author_avatar_url,omitempty"`
	Body               string    `json:"body"`
	ReplyTo            string    `json:"reply_to,omitempty"`
	// ThreadRoot is the id of the thread's root comment (== own ID for
	// roots). Grouping key for the whole conversation, so replies to
	// replies still land in one visual thread.
	ThreadRoot string `json:"thread_root,omitempty"`
	Resolved   bool   `json:"resolved"`
	// Mentions names who the comment is FOR (user ids or librarian roles,
	// e.g. "detective"). Only mentioned users get a review request.
	Mentions  []string  `json:"mentions,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const commentSelect = `
	SELECT c.id, c.entry_id, c.author_user_id,
	       COALESCE(u.name, '(unknown)'),
	       CASE WHEN u.role = 'agent' THEN 'agent' ELSE 'human' END,
	       COALESCE(u.librarian_role, ''),
	       COALESCE(u.avatar_url, ''),
	       c.body, COALESCE(c.reply_to, ''), COALESCE(c.thread_root, c.id), c.resolved,
	       COALESCE(c.mentions, ''),
	       c.created_at, c.updated_at
	  FROM entry_comments c
	  LEFT JOIN users u ON u.id = c.author_user_id`

func scanComment(sc scanner) (*EntryComment, error) {
	var c EntryComment
	var mentions string
	if err := sc.Scan(&c.ID, &c.EntryID, &c.AuthorUserID, &c.AuthorName,
		&c.AuthorKind, &c.AuthorLibrarianRole, &c.AuthorAvatarURL,
		&c.Body, &c.ReplyTo, &c.ThreadRoot,
		&c.Resolved, &mentions, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if mentions != "" {
		_ = json.Unmarshal([]byte(mentions), &c.Mentions)
	}
	return &c, nil
}

// normalizeMentions trims, drops blanks, and de-dups mention tokens.
func normalizeMentions(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range in {
		m = strings.TrimSpace(strings.TrimPrefix(m, "@"))
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// CreateComment inserts a comment on entryID by authorUserID. replyTo, if
// non-empty, must be an existing comment ON THE SAME ENTRY (one cannot
// thread across entries). Returns the created comment with author joined.
func (s *Store) CreateComment(ctx context.Context, entryID, authorUserID, body, replyTo string, mentions []string) (*EntryComment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, errors.New("comment body required")
	}
	// Replying to ANY comment in a thread is allowed (including a
	// reply); the new comment inherits the parent's thread_root so the
	// whole conversation stays grouped under one key.
	threadRoot := ""
	if replyTo != "" {
		var parentEntry, parentThread string
		err := s.db.QueryRowContext(ctx,
			`SELECT entry_id, COALESCE(thread_root, id) FROM entry_comments WHERE id = ?`,
			replyTo).Scan(&parentEntry, &parentThread)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if parentEntry != entryID {
			return nil, errors.New("reply_to belongs to a different entry")
		}
		threadRoot = parentThread
	}
	id := newLibrarianID("C")
	if threadRoot == "" {
		threadRoot = id // a fresh root starts its own thread
	}
	var replyArg any
	if replyTo != "" {
		replyArg = replyTo
	}
	var mentionsArg any
	if m := normalizeMentions(mentions); len(m) > 0 {
		b, _ := json.Marshal(m)
		mentionsArg = string(b)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO entry_comments(id, entry_id, author_user_id, body, reply_to, thread_root, mentions)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, entryID, authorUserID, body, replyArg, threadRoot, mentionsArg); err != nil {
		return nil, err
	}
	return s.GetComment(ctx, id)
}

// GetComment fetches one comment by id (author joined).
func (s *Store) GetComment(ctx context.Context, id string) (*EntryComment, error) {
	c, err := scanComment(s.db.QueryRowContext(ctx, commentSelect+` WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// ListComments returns all comments on an entry, oldest first, so the UI
// can render threads in posting order.
func (s *Store) ListComments(ctx context.Context, entryID string) ([]*EntryComment, error) {
	rows, err := s.db.QueryContext(ctx, commentSelect+`
		WHERE c.entry_id = ?
		ORDER BY c.created_at ASC`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*EntryComment
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateComment edits body and/or resolved. Nil args are left unchanged.
// Returns the updated comment.
func (s *Store) UpdateComment(ctx context.Context, id string, body *string, resolved *bool) (*EntryComment, error) {
	sets := []string{}
	args := []any{}
	if body != nil {
		b := strings.TrimSpace(*body)
		if b == "" {
			return nil, errors.New("comment body cannot be blank")
		}
		sets = append(sets, "body = ?")
		args = append(args, b)
	}
	if resolved != nil {
		sets = append(sets, "resolved = ?")
		args = append(args, *resolved)
	}
	if len(sets) == 0 {
		return s.GetComment(ctx, id)
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, id)
	res, err := s.db.ExecContext(ctx,
		`UPDATE entry_comments SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetComment(ctx, id)
}

// DeleteComment removes a comment (and, via ON DELETE CASCADE, its replies).
func (s *Store) DeleteComment(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM entry_comments WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecentComment is one comment joined with the minimal entry context a
// responder agent needs to decide "is this addressed to my entry, and
// have I answered it yet?" without a per-entry round-trip.
type RecentComment struct {
	Comment        *EntryComment `json:"comment"`
	EntryTitle     string        `json:"entry_title"`
	EntryType      string        `json:"entry_type"`
	EntryCreatedBy string        `json:"entry_created_by"`
}

// ListRecentComments returns comments newest-first across ALL entries,
// optionally filtered to entries created by entryCreatedBy and/or to
// comments strictly after `since` (RFC3339 or SQLite datetime). This is
// the cross-entry feed that lets an agent poll "new comments on my
// entries" with one call instead of walking every entry it authored.
func (s *Store) ListRecentComments(ctx context.Context, entryCreatedBy, since string, limit int) ([]*RecentComment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := commentSelect + `
	  JOIN entries e ON e.id = c.entry_id`
	where := []string{}
	args := []any{}
	if entryCreatedBy != "" {
		where = append(where, "e.created_by = ?")
		args = append(args, entryCreatedBy)
	}
	if since != "" {
		where = append(where, "c.created_at > ?")
		args = append(args, since)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY c.created_at DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RecentComment
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, &RecentComment{Comment: c})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, rc := range out {
		_ = s.db.QueryRowContext(ctx,
			`SELECT title, type, COALESCE(created_by,'') FROM entries WHERE id = ?`,
			rc.Comment.EntryID).Scan(&rc.EntryTitle, &rc.EntryType, &rc.EntryCreatedBy)
	}
	return out, nil
}

// reviewRequestWhere matches unresolved comments that @mention the user
// (by id or by their librarian role) and were written by someone else.
// The two bound params are both userID; the role is resolved inline.
const reviewRequestWhere = `
	c.resolved = 0
	AND c.author_user_id != ?
	AND c.mentions IS NOT NULL
	AND EXISTS (
		SELECT 1 FROM json_each(c.mentions) je
		WHERE je.value = ?
		   OR je.value = (SELECT librarian_role FROM users WHERE id = ?)
	)`

// CountReviewRequests returns how many open review requests mention userID.
// Cheap enough to call per-request from the response-header middleware.
func (s *Store) CountReviewRequests(ctx context.Context, userID string) (int, error) {
	if userID == "" {
		return 0, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM entry_comments c WHERE `+reviewRequestWhere,
		userID, userID, userID).Scan(&n)
	return n, err
}

// ReviewRequest is one open comment addressed to the caller, with the
// minimal entry context needed to act on it.
type ReviewRequest struct {
	Comment    *EntryComment `json:"comment"`
	EntryTitle string        `json:"entry_title"`
	EntryType  string        `json:"entry_type"`
}

// ListReviewRequests returns the open review requests mentioning userID,
// oldest first (FIFO — address the longest-waiting first).
func (s *Store) ListReviewRequests(ctx context.Context, userID string, limit int) ([]*ReviewRequest, error) {
	if userID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, commentSelect+`
		WHERE `+reviewRequestWhere+`
		ORDER BY c.created_at ASC
		LIMIT ?`, userID, userID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ReviewRequest
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, &ReviewRequest{Comment: c})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Attach entry title/type per request (small N, one cheap query each).
	for _, rr := range out {
		_ = s.db.QueryRowContext(ctx,
			`SELECT title, type FROM entries WHERE id = ?`, rr.Comment.EntryID).
			Scan(&rr.EntryTitle, &rr.EntryType)
	}
	return out, nil
}
