package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Directive is one operator instruction to a librarian role ("watch
// this topic"), registered from the UI. Directives ADD to a role's
// standing criteria — consumers must never treat them as replacing
// the normal selection bar.
type Directive struct {
	ID            string    `json:"id"`
	Role          string    `json:"role"`
	Text          string    `json:"text"`
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	Active        bool      `json:"active"`
}

// CreateDirective registers a new directive for role.
func (s *Store) CreateDirective(ctx context.Context, role, text, createdBy string) (*Directive, error) {
	role = strings.TrimSpace(role)
	text = strings.TrimSpace(text)
	if !ValidLibrarianRole(role) && role != "chronicler" {
		return nil, fmt.Errorf("%w: invalid role %q", ErrInvalidInput, role)
	}
	if text == "" {
		return nil, fmt.Errorf("%w: text required", ErrInvalidInput)
	}
	id := newLibrarianID("d")
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO librarian_directives(id, role, text, created_by)
		VALUES (?, ?, ?, ?)`, id, role, text, createdBy); err != nil {
		return nil, translateErr(err)
	}
	return s.GetDirective(ctx, id)
}

// GetDirective fetches one directive (creator name joined).
func (s *Store) GetDirective(ctx context.Context, id string) (*Directive, error) {
	var d Directive
	err := s.db.QueryRowContext(ctx, `
		SELECT d.id, d.role, d.text, d.created_by, COALESCE(u.name,''), d.created_at, d.active
		  FROM librarian_directives d LEFT JOIN users u ON u.id = d.created_by
		 WHERE d.id = ?`, id).
		Scan(&d.ID, &d.Role, &d.Text, &d.CreatedBy, &d.CreatedByName, &d.CreatedAt, &d.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ListDirectives returns directives, newest first. role filters when
// non-empty; activeOnly hides deactivated ones.
func (s *Store) ListDirectives(ctx context.Context, role string, activeOnly bool) ([]*Directive, error) {
	q := `SELECT d.id, d.role, d.text, d.created_by, COALESCE(u.name,''), d.created_at, d.active
		  FROM librarian_directives d LEFT JOIN users u ON u.id = d.created_by WHERE 1=1`
	args := []any{}
	if role != "" {
		q += " AND d.role = ?"
		args = append(args, role)
	}
	if activeOnly {
		q += " AND d.active = 1"
	}
	q += " ORDER BY d.created_at DESC LIMIT 200"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[Directive](rows, func(c rowScanner, d *Directive) error {
		return c.Scan(&d.ID, &d.Role, &d.Text, &d.CreatedBy, &d.CreatedByName, &d.CreatedAt, &d.Active)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*Directive, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// SetDirectiveActive toggles a directive.
func (s *Store) SetDirectiveActive(ctx context.Context, id string, active bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE librarian_directives SET active = ? WHERE id = ?`, active, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteDirective removes a directive permanently.
func (s *Store) DeleteDirective(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM librarian_directives WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
