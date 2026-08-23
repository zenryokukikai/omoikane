package store

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"testing"
	"time"
)

// Fault-injection harness shared by the fault_* test files, plus the tests
// for injected environment/dependency faults: crypto/rand failures, a
// closed *sql.DB, an unwritable DSN, and broken migration filesystems.
//
// Sibling aspect files:
//   - fault_corruption_test.go — schema/data corruption via SQL (dropped
//     tables, view swaps, corrupt rows).
//   - fault_validation_test.go — validation failures and clean-path branch
//     coverage.

// seed creates a project and entry, then returns the store + entry ID so
// fault-injection tests can clobber state and exercise error paths.
func seed(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	if err := s.CreateProject(ctx, &Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "x", Body: "y",
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, id
}

// ---- crypto/rand failure path ----

func TestNewEntryIDRandError(t *testing.T) {
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	randRead = func([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
	if _, err := newEntryID("trap"); err == nil {
		t.Fatal("expected rand error")
	}
}

func TestCreateEntryRandError(t *testing.T) {
	s, _ := seed(t)
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	randRead = func([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
	_, err := s.CreateEntry(context.Background(), &Entry{
		ProjectID: "p", Type: "trap", Title: "x", Body: "y",
	})
	if err == nil {
		t.Fatal("expected rand error")
	}
}

// ---- ID collision retry exhaustion ----

func TestCreateEntryCollisionExhausted(t *testing.T) {
	s, _ := seed(t)
	// Force randRead to always produce the same bytes so newEntryID always
	// returns the same ID. seed already inserted one entry with random
	// bytes; the deterministic ID we now generate will probably differ —
	// but every successive call returns the *same* ID, so the second
	// attempt collides with the first.
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	randRead = func(b []byte) (int, error) {
		// fixed pattern → always the same ID
		for i := range b {
			b[i] = 0
		}
		return len(b), nil
	}
	// First call seeds a new ID (collides nothing because seed's entry
	// used real random). Second call → collision → retry. We need 5
	// collisions to hit the "failed to allocate" path.
	if _, err := s.CreateEntry(context.Background(), &Entry{
		ProjectID: "p", Type: "decision", Title: "first", Body: "b",
	}); err != nil {
		t.Fatal(err)
	}
	// Second insert: same ID → collision → retry 5× → fail.
	_, err := s.CreateEntry(context.Background(), &Entry{
		ProjectID: "p", Type: "decision", Title: "second", Body: "b",
	})
	if err == nil {
		t.Fatal("expected ID-allocation exhaustion")
	}
}

// ---- closed-DB shotgun ----

// closedStore returns a store whose underlying *sql.DB has been closed.
// Every subsequent operation fails at the first DB call.
func closedStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	return s
}

func TestClosedStoreAllOperationsFail(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		run  func(*Store) error
	}{
		{"CreateProject", func(s *Store) error {
			return s.CreateProject(ctx, &Project{ID: "x", Name: "x"})
		}},
		{"GetProject", func(s *Store) error {
			_, err := s.GetProject(ctx, "x")
			return err
		}},
		{"ListProjects", func(s *Store) error {
			_, err := s.ListProjects(ctx)
			return err
		}},
		{"CreateEntry", func(s *Store) error {
			_, err := s.CreateEntry(ctx, &Entry{ProjectID: "x", Type: "trap", Title: "t", Body: "b"})
			return err
		}},
		{"GetEntry", func(s *Store) error {
			_, err := s.GetEntry(ctx, "x")
			return err
		}},
		{"GetEntryAsOf", func(s *Store) error {
			_, err := s.GetEntryAsOf(ctx, "x", time.Now())
			return err
		}},
		{"ListEntries", func(s *Store) error {
			_, _, err := s.ListEntries(ctx, EntryFilter{})
			return err
		}},
		{"UpdateEntry", func(s *Store) error {
			title := "x"
			_, _, err := s.UpdateEntry(ctx, "x", EntryPatch{Title: &title})
			return err
		}},
		{"SoftDeleteEntry", func(s *Store) error {
			return s.SoftDeleteEntry(ctx, "x", "", "")
		}},
		{"EntryHistory", func(s *Store) error {
			_, err := s.EntryHistory(ctx, "x")
			return err
		}},
		{"ReplaceTags", func(s *Store) error {
			return s.ReplaceTags(ctx, "x", []string{"a"}, "human")
		}},
		{"SetEnrichment", func(s *Store) error {
			return s.SetEnrichment(ctx, "x", 1)
		}},
		{"SearchFTS", func(s *Store) error {
			_, _, err := s.SearchFTS(ctx, `"x"*`, EntryFilter{})
			return err
		}},
		{"WriteAudit", func(s *Store) error {
			return s.WriteAudit(ctx, AuditEvent{Method: "GET", Path: "/", StatusCode: 200})
		}},
		{"CreateUser", func(s *Store) error {
			return s.CreateUser(ctx, &User{ID: "u", Name: "u"})
		}},
		{"GetUser", func(s *Store) error {
			_, err := s.GetUser(ctx, "u")
			return err
		}},
		{"CreateToken", func(s *Store) error {
			_, err := s.CreateToken(ctx, "u", "t", []string{"read"}, nil)
			return err
		}},
		{"LookupToken", func(s *Store) error {
			_, err := s.LookupToken(ctx, "plain")
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := closedStore(t)
			if err := c.run(s); err == nil {
				t.Fatalf("%s should fail on closed store", c.name)
			}
		})
	}
}

// ---- dropped-table helpers ----

// dropAfterSeed seeds, then drops the named table so subsequent reads of
// it fail. The store's other tables remain intact, letting us probe
// intermediate error branches. FK constraints are disabled in the test
// session to allow dropping referenced tables.
func dropAfterSeed(t *testing.T, drop string) (*Store, string) {
	t.Helper()
	s, id := seed(t)
	if _, err := s.DB().Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DROP TABLE ` + drop); err != nil {
		t.Fatal(err)
	}
	return s, id
}

func dropTable(t *testing.T, s *Store, name string) {
	t.Helper()
	if _, err := s.DB().Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DROP TABLE ` + name); err != nil {
		t.Fatal(err)
	}
}

// ---- Open / migrate error branches ----

func TestOpenInvalidDSN(t *testing.T) {
	if _, err := Open(context.Background(), "/dev/null/cannot/exist.db"); err == nil {
		t.Fatal("expected error for unwritable path")
	}
}

func TestMigrationsRunOnceWhenReopened(t *testing.T) {
	// First open applies all migrations; second open should detect them
	// as already applied and short-circuit. We close the first store and
	// reopen on the same path.
	dir := t.TempDir()
	path := filepath.Join(dir, "remig.db")
	s1, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()
	s2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	var n int
	if err := s2.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("expected >=2 migrations, got %d", n)
	}
}

// ---- GenerateToken rand failure ----

func TestGenerateTokenRandError(t *testing.T) {
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	randRead = func([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
	if _, err := GenerateToken(); err == nil {
		t.Fatal("expected error")
	}
}

func TestCreateTokenRandError(t *testing.T) {
	s, _ := seed(t)
	_ = s.CreateUser(context.Background(), &User{ID: "u", Name: "u"})
	orig := randRead
	t.Cleanup(func() { randRead = orig })
	randRead = func([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
	if _, err := s.CreateToken(context.Background(), "u", "n", []string{"read"}, nil); err == nil {
		t.Fatal("expected error")
	}
}

// ---- migrate fault injection via fs override ----

// brokenMigFS is an fs.FS whose ReadDir / Open / ReadFile selectively fails.
type brokenMigFS struct {
	readDirErr  error
	readFileErr error
	statements  string // SQL returned by ReadFile when readFileErr is nil
}

func (b brokenMigFS) Open(name string) (fs.File, error) {
	if b.readFileErr != nil {
		return nil, b.readFileErr
	}
	return migrationsFS.Open(name)
}

// Implement fs.ReadDirFS to control ReadDir behavior.
func (b brokenMigFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if b.readDirErr != nil {
		return nil, b.readDirErr
	}
	return fs.ReadDir(migrationsFS, name)
}

// Implement fs.ReadFileFS so fs.ReadFile uses our override.
func (b brokenMigFS) ReadFile(name string) ([]byte, error) {
	if b.readFileErr != nil {
		return nil, b.readFileErr
	}
	if b.statements != "" {
		return []byte(b.statements), nil
	}
	return fs.ReadFile(migrationsFS, name)
}

func TestMigrateReadDirError(t *testing.T) {
	orig := migrationFS
	t.Cleanup(func() { migrationFS = orig })
	migrationFS = brokenMigFS{readDirErr: io.ErrUnexpectedEOF}
	dir := t.TempDir()
	if _, err := Open(context.Background(), filepath.Join(dir, "x.db")); err == nil {
		t.Fatal("expected error")
	}
}

func TestMigrateReadFileError(t *testing.T) {
	orig := migrationFS
	t.Cleanup(func() { migrationFS = orig })
	migrationFS = brokenMigFS{readFileErr: io.ErrUnexpectedEOF}
	dir := t.TempDir()
	if _, err := Open(context.Background(), filepath.Join(dir, "x.db")); err == nil {
		t.Fatal("expected error")
	}
}

func TestMigrateApplySQLError(t *testing.T) {
	orig := migrationFS
	t.Cleanup(func() { migrationFS = orig })
	migrationFS = brokenMigFS{statements: `THIS IS NOT VALID SQL;`}
	dir := t.TempDir()
	if _, err := Open(context.Background(), filepath.Join(dir, "x.db")); err == nil {
		t.Fatal("expected SQL error")
	}
}

// ---- migrate / parsing skip branches ----

// nonSQLFileFS layers an extra non-.sql entry, a directory entry, and a
// file whose prefix is non-numeric to exercise the parse-skip branches of
// migrate().
type augmentedMigFS struct{}

func (augmentedMigFS) Open(name string) (fs.File, error) {
	return migrationsFS.Open(name)
}

func (augmentedMigFS) ReadDir(name string) ([]fs.DirEntry, error) {
	real, err := fs.ReadDir(migrationsFS, name)
	if err != nil {
		return nil, err
	}
	return append(real, fakeEntry{name: "notsql.txt"},
		fakeEntry{name: "subdir", isDir: true},
		fakeEntry{name: "no-prefix.sql"},
		fakeEntry{name: "abc_bad.sql"}), nil
}

func (augmentedMigFS) ReadFile(name string) ([]byte, error) {
	return fs.ReadFile(migrationsFS, name)
}

type fakeEntry struct {
	name  string
	isDir bool
}

func (f fakeEntry) Name() string               { return f.name }
func (f fakeEntry) IsDir() bool                { return f.isDir }
func (f fakeEntry) Type() fs.FileMode          { return 0 }
func (f fakeEntry) Info() (fs.FileInfo, error) { return nil, nil }

func TestMigrateParsingSkipsNonMigrations(t *testing.T) {
	orig := migrationFS
	t.Cleanup(func() { migrationFS = orig })
	migrationFS = augmentedMigFS{}
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	_ = s.Close()
}

// ---- migrate / tx.Commit defensive branches ----
//
// The following branches in store.go and entries.go are intentionally left
// uncovered:
//
//   - migrate(): CREATE TABLE schema_migrations failure, SELECT version
//     failure, rows.Scan / rows.Close errors on the schema_migrations
//     read, BeginTx failure, INSERT-into-schema_migrations failure, and
//     tx.Commit() failure inside the migration loop.
//   - CreateEntry / UpdateEntry / SoftDeleteEntry: tx.Commit() failure.
//   - LookupToken: the post-success UPDATE api_tokens last_used_at
//     failure.
//
// Each is a defensive guard against a DB-driver fault that, by the time
// the code path is reached, has been shown to be operational by an earlier
// successful query. Triggering them would require either a per-method mock
// of *sql.DB / *sql.Tx — which contradicts §2's "internal-only, low attack
// surface, dependency-minimal" principle by introducing a substantial
// abstraction layer — or a custom sqlite3 driver that fails on specific
// SQL strings, which is too intrusive for fault-injection.
//
// These branches add value (they correctly surface unexpected DB faults if
// they ever occur) but are not exercisable through public API tests; we
// document the rationale here rather than delete the safety code.

// ensure errors import stays referenced even if some tests are pruned
var _ = errors.New
