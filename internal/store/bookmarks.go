package store

import (
	"context"
	"time"
)

// Bookmark is one entry a human starred for later, with enough entry
// context to render a shortlist without extra round-trips.
type Bookmark struct {
	EntryID    string    `json:"entry_id"`
	CreatedAt  time.Time `json:"created_at"`
	EntryTitle string    `json:"entry_title"`
	EntryType  string    `json:"entry_type"`
	ProjectID  string    `json:"project_id"`
	Status     string    `json:"status"`
}

// AddBookmark stars an entry for userID. Idempotent — starring twice
// keeps the original timestamp.
func (s *Store) AddBookmark(ctx context.Context, userID, entryID string) error {
	if _, err := s.GetEntry(ctx, entryID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_bookmarks(user_id, entry_id) VALUES (?, ?)
		ON CONFLICT(user_id, entry_id) DO NOTHING`, userID, entryID)
	return translateErr(err)
}

// RemoveBookmark unstars. Removing a non-bookmark is a no-op, not an
// error — the end state is what the user asked for. A hidden entry is a
// 404 like every other entry write (fail-closed; ListBookmarks hides
// stale rows regardless).
func (s *Store) RemoveBookmark(ctx context.Context, userID, entryID string) error {
	if err := requireVisibleEntry(ctx, s.db, entryID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM user_bookmarks WHERE user_id = ? AND entry_id = ?`,
		userID, entryID)
	return translateErr(err)
}

// IsBookmarked reports whether userID starred entryID.
func (s *Store) IsBookmarked(ctx context.Context, userID, entryID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_bookmarks WHERE user_id = ? AND entry_id = ?`,
		userID, entryID).Scan(&n)
	return n > 0, err
}

// ListBookmarks returns userID's bookmarks, newest first, joined with
// the entry fields a shortlist needs.
func (s *Store) ListBookmarks(ctx context.Context, userID string, limit int) ([]*Bookmark, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := `
		SELECT b.entry_id, b.created_at, e.title, e.type, e.project_id, e.status
		  FROM user_bookmarks b
		  JOIN entries e ON e.id = b.entry_id
		 WHERE b.user_id = ?`
	args := []any{userID}
	// Bookmarks on entries outside the caller's visible spaces are
	// hidden rows, not errors (membership can be revoked after starring).
	if cond, condArgs := spaceCond(ctx, "e"); cond != "" {
		q += " AND " + cond
		args = append(args, condArgs...)
	}
	q += `
		 ORDER BY b.created_at DESC
		 LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[Bookmark](rows, func(c rowScanner, b *Bookmark) error {
		return c.Scan(&b.EntryID, &b.CreatedAt, &b.EntryTitle, &b.EntryType,
			&b.ProjectID, &b.Status)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*Bookmark, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}
