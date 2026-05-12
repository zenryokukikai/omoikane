package store

import (
	"context"
	"database/sql"
	"time"
)

func (s *Store) CreateProject(ctx context.Context, p *Project) error {
	if p.ID == "" || p.Name == "" {
		return ErrInvalidInput
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO projects(id, name, description, metadata, created_at) VALUES (?, ?, ?, ?, ?)`,
		p.ID, p.Name, nullable(p.Description), nullable(p.Metadata), now)
	if err != nil {
		return translateErr(err)
	}
	p.CreatedAt = now
	return nil
}

func (s *Store) GetProject(ctx context.Context, id string) (*Project, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, COALESCE(description,''), created_at, COALESCE(metadata,'')
		 FROM projects WHERE id = ?`, id)
	var p Project
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &p.CreatedAt, &p.Metadata); err != nil {
		return nil, translateErr(err)
	}
	return &p, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]*Project, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, COALESCE(description,''), created_at, COALESCE(metadata,'')
		 FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[Project](rows, func(c rowScanner, p *Project) error {
		return c.Scan(&p.ID, &p.Name, &p.Description, &p.CreatedAt, &p.Metadata)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*Project, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// nullable converts an empty string to a SQL NULL so writes preserve the
// distinction between "" (intentional) and unset (NULL).
func nullable(s string) any {
	if s == "" {
		return sql.NullString{}
	}
	return s
}
