package store

import (
	"context"
	"time"
)

// UserLibrarian is one user's personal librarian configuration (issue
// #73). The agent itself lives on the opencrab runtime; this row is
// omoikane's record of what was provisioned (and with what name /
// persona) so the settings page can re-render and re-provision.
type UserLibrarian struct {
	UserID    string    `json:"user_id"`
	AgentID   string    `json:"agent_id"`
	Name      string    `json:"name"`
	Persona   string    `json:"persona"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// GetUserLibrarian returns the personal librarian row for userID, or
// ErrNotFound when the user has not set one up.
func (s *Store) GetUserLibrarian(ctx context.Context, userID string) (*UserLibrarian, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT user_id, agent_id, name, persona, status, created_at
		  FROM user_librarians WHERE user_id = ?`, userID)
	var ul UserLibrarian
	if err := row.Scan(&ul.UserID, &ul.AgentID, &ul.Name, &ul.Persona,
		&ul.Status, &ul.CreatedAt); err != nil {
		return nil, translateErr(err)
	}
	return &ul, nil
}

// UpsertUserLibrarian creates or updates the user's personal librarian
// row. Idempotent — re-saving keeps the original created_at.
func (s *Store) UpsertUserLibrarian(ctx context.Context, ul *UserLibrarian) error {
	if ul.UserID == "" || ul.AgentID == "" || ul.Name == "" {
		return ErrInvalidInput
	}
	if ul.Status == "" {
		ul.Status = "active"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_librarians(user_id, agent_id, name, persona, status)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			agent_id = excluded.agent_id,
			name     = excluded.name,
			persona  = excluded.persona,
			status   = excluded.status`,
		ul.UserID, ul.AgentID, ul.Name, ul.Persona, ul.Status)
	return translateErr(err)
}

// HasAPIToken reports whether userID holds a live (non-expired) API
// token with the given name. Used by the personal-librarian flow as the
// idempotency check: the token row's existence IS the "already issued"
// flag — no separate column to drift out of sync.
func (s *Store) HasAPIToken(ctx context.Context, userID, name string) (bool, error) {
	// Expiry is evaluated in Go, not SQL: the driver stores expires_at
	// as RFC3339 ("…T…Z") while CURRENT_TIMESTAMP renders "YYYY-MM-DD
	// HH:MM:SS", so a string comparison between the two lies. Same
	// convention as LookupToken.
	rows, err := s.db.QueryContext(ctx, `
		SELECT expires_at FROM api_tokens
		 WHERE user_id = ? AND name = ?
		   AND COALESCE(token_type,'api') = 'api'`, userID, name)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	now := time.Now()
	for rows.Next() {
		var exp nullTimeBox
		if err := rows.Scan(&exp); err != nil {
			return false, err
		}
		if !exp.Valid || exp.Time.After(now) {
			return true, nil
		}
	}
	return false, rows.Err()
}
