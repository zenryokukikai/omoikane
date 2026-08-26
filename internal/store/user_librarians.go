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
	Icon      string    `json:"icon"`      // short text icon (emoji); "" → built-in default
	IconMime  string    `json:"icon_mime"` // non-empty ⇔ an uploaded image exists
	IconVer   int64     `json:"icon_ver"`  // bumped per image change; cache-busts the serving URL
	CreatedAt time.Time `json:"created_at"`
	// GateInstanceID is the external gate instance registered for this
	// librarian (issue #104 G2, UUIDv7); "" = not registered. Written
	// via SetUserLibrarianGateInstance, never via Upsert.
	GateInstanceID string `json:"gate_instance_id"`
}

// IconText is the text icon to render when no image is uploaded.
// (The image serving URL is built dashboard-side — it needs the
// request's auth token, which the store cannot know.)
func (ul *UserLibrarian) IconText() string {
	if ul.Icon != "" {
		return ul.Icon
	}
	return "🤖"
}

// GetUserLibrarian returns the personal librarian row for userID, or
// ErrNotFound when the user has not set one up.
func (s *Store) GetUserLibrarian(ctx context.Context, userID string) (*UserLibrarian, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT user_id, agent_id, name, persona, status,
		       icon, icon_mime, icon_ver, created_at, gate_instance_id
		  FROM user_librarians WHERE user_id = ?`, userID)
	var ul UserLibrarian
	if err := row.Scan(&ul.UserID, &ul.AgentID, &ul.Name, &ul.Persona,
		&ul.Status, &ul.Icon, &ul.IconMime, &ul.IconVer, &ul.CreatedAt,
		&ul.GateInstanceID); err != nil {
		return nil, translateErr(err)
	}
	return &ul, nil
}

// ListActiveUserLibrarians returns every ACTIVE personal librarian —
// the gate binary's connection roster (issue #104 G3a, served via
// GET /v1/gateway/librarians). Rows whose gate_instance_id is still
// empty are included: whether an instance is connectable is the
// caller's call, not a hidden filter here. Ordered by user_id for
// stable output. Icon blobs are not fetched.
func (s *Store) ListActiveUserLibrarians(ctx context.Context) ([]*UserLibrarian, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id, agent_id, name, status, gate_instance_id
		  FROM user_librarians WHERE status = 'active'
		 ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*UserLibrarian
	for rows.Next() {
		var ul UserLibrarian
		if err := rows.Scan(&ul.UserID, &ul.AgentID, &ul.Name, &ul.Status,
			&ul.GateInstanceID); err != nil {
			return nil, err
		}
		out = append(out, &ul)
	}
	return out, rows.Err()
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
		INSERT INTO user_librarians(user_id, agent_id, name, persona, status, icon)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			agent_id = excluded.agent_id,
			name     = excluded.name,
			persona  = excluded.persona,
			status   = excluded.status,
			icon     = excluded.icon`,
		ul.UserID, ul.AgentID, ul.Name, ul.Persona, ul.Status, ul.Icon)
	return translateErr(err)
}

// SetUserLibrarianIconImage stores (or replaces) the uploaded icon
// image. mime must already be validated by the caller. Bumps icon_ver
// so the serving URL changes and browser caches refresh.
func (s *Store) SetUserLibrarianIconImage(ctx context.Context, userID string, img []byte, mime string) error {
	if userID == "" || len(img) == 0 || mime == "" {
		return ErrInvalidInput
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE user_librarians
		   SET icon_image = ?, icon_mime = ?, icon_ver = icon_ver + 1
		 WHERE user_id = ?`, img, mime, userID)
	if err != nil {
		return translateErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearUserLibrarianIconImage removes the uploaded icon image (the
// display falls back to the text icon). No-op when none is set.
func (s *Store) ClearUserLibrarianIconImage(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE user_librarians
		   SET icon_image = NULL, icon_mime = '', icon_ver = icon_ver + 1
		 WHERE user_id = ?`, userID)
	return translateErr(err)
}

// GetUserLibrarianIconImage returns the uploaded icon image and its
// mime type. ErrNotFound when the user has no librarian or no image.
func (s *Store) GetUserLibrarianIconImage(ctx context.Context, userID string) ([]byte, string, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT icon_image, icon_mime FROM user_librarians
		 WHERE user_id = ? AND icon_mime != ''`, userID)
	var img []byte
	var mime string
	if err := row.Scan(&img, &mime); err != nil {
		return nil, "", translateErr(err)
	}
	return img, mime, nil
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
