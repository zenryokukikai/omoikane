package store

import (
	"context"
	"database/sql"
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
	Body               string    `json:"body"`
	ReplyTo            string    `json:"reply_to,omitempty"`
	Resolved           bool      `json:"resolved"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

const commentSelect = `
	SELECT c.id, c.entry_id, c.author_user_id,
	       COALESCE(u.name, '(unknown)'),
	       CASE WHEN u.role = 'agent' THEN 'agent' ELSE 'human' END,
	       COALESCE(u.librarian_role, ''),
	       c.body, COALESCE(c.reply_to, ''), c.resolved,
	       c.created_at, c.updated_at
	  FROM entry_comments c
	  LEFT JOIN users u ON u.id = c.author_user_id`

func scanComment(sc scanner) (*EntryComment, error) {
	var c EntryComment
	if err := sc.Scan(&c.ID, &c.EntryID, &c.AuthorUserID, &c.AuthorName,
		&c.AuthorKind, &c.AuthorLibrarianRole, &c.Body, &c.ReplyTo,
		&c.Resolved, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// CreateComment inserts a comment on entryID by authorUserID. replyTo, if
// non-empty, must be an existing comment ON THE SAME ENTRY (one cannot
// thread across entries). Returns the created comment with author joined.
func (s *Store) CreateComment(ctx context.Context, entryID, authorUserID, body, replyTo string) (*EntryComment, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, errors.New("comment body required")
	}
	if replyTo != "" {
		var parentEntry string
		err := s.db.QueryRowContext(ctx,
			`SELECT entry_id FROM entry_comments WHERE id = ?`, replyTo).Scan(&parentEntry)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if parentEntry != entryID {
			return nil, errors.New("reply_to belongs to a different entry")
		}
	}
	id := newLibrarianID("C")
	var replyArg any
	if replyTo != "" {
		replyArg = replyTo
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO entry_comments(id, entry_id, author_user_id, body, reply_to)
		VALUES (?, ?, ?, ?, ?)`,
		id, entryID, authorUserID, body, replyArg); err != nil {
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
