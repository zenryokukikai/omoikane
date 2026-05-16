// Package store is the persistence layer. It owns the SQLite connection,
// runs migrations, and exposes typed CRUD methods. The store enforces
// integrity (enum validation, OCC) but not business rules — those live in
// internal/api.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed all:migrations
var migrationsFS embed.FS

// migrationFS is overridable in tests so the otherwise-unreachable file
// system / SQL apply errors in migrate() can be exercised by injecting a
// broken or hostile FS.
var migrationFS fs.FS = migrationsFS

type Store struct {
	db *sql.DB
	// dataDir is the directory containing the SQLite file. Used as the
	// root for non-SQL persistent state — currently just attachment
	// blobs at <dataDir>/attachments/<hash[:2]>/<hash[2:4]>/<hash>.
	dataDir string
}

// Open opens the SQLite DB at path with WAL mode and runs pending
// migrations. sql.Open for the sqlite3 driver never returns an error at
// the API level (it only validates the DSN format, which we control), so
// the only failure modes that can surface here are the eventual ping or
// migrate step.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := path + "?_journal=WAL&_busy_timeout=5000&_fk=1"
	db, _ := sql.Open("sqlite3", dsn)
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	s := &Store{db: db, dataDir: filepath.Dir(path)}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for tests.
func (s *Store) DB() *sql.DB { return s.db }

// DataDir returns the directory containing the SQLite file. Used by
// non-SQL persistence (attachment blobs).
func (s *Store) DataDir() string { return s.dataDir }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	versions, err := mapRows[int](rows, func(c rowScanner, v *int) error {
		return c.Scan(v)
	})
	if err != nil {
		return err
	}
	applied := make(map[int]bool, len(versions))
	for _, v := range versions {
		applied[v] = true
	}

	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	type mig struct {
		version int
		name    string
	}
	var migs []mig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		under := strings.IndexByte(e.Name(), '_')
		if under <= 0 {
			continue
		}
		v, err := strconv.Atoi(e.Name()[:under])
		if err != nil {
			continue
		}
		migs = append(migs, mig{v, e.Name()})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })

	for _, m := range migs {
		if applied[m.version] {
			continue
		}
		sqlBytes, err := fs.ReadFile(migrationFS, "migrations/"+m.name)
		if err != nil {
			return fmt.Errorf("read %s: %w", m.name, err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version) VALUES(?)`, m.version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record %s: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", m.name, err)
		}
	}
	return nil
}

// translateErr maps driver errors into store sentinels. Unique/PK violations
// become ErrAlreadyExists; sql.ErrNoRows becomes ErrNotFound.
func translateErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	msg := err.Error()
	if strings.Contains(msg, "UNIQUE constraint") || strings.Contains(msg, "PRIMARY KEY") {
		return ErrAlreadyExists
	}
	return err
}
