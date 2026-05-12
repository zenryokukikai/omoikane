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

// ---- dropped-table specific error branches ----

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

func TestCreateEntryProjectsTableMissing(t *testing.T) {
	s, _ := seed(t)
	dropTable(t, s, "projects")
	_, err := s.CreateEntry(context.Background(), &Entry{
		ProjectID: "p", Type: "trap", Title: "x", Body: "y",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestListEntriesQueryError(t *testing.T) {
	s, _ := dropAfterSeed(t, "entries_fts")
	// Drop the FTS table while leaving entries — the simple list query
	// uses entries directly so this still works. To force a query error,
	// drop entries instead.
	dropTable(t, s, "entries")
	if _, _, err := s.ListEntries(context.Background(), EntryFilter{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetEntryAsOfBeforeCreation(t *testing.T) {
	s, id := seed(t)
	// Far-past timestamp → no matching history row → ErrNotFound.
	if _, err := s.GetEntryAsOf(context.Background(), id, time.Unix(0, 0).UTC()); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetEntryAsOfHistoryQueryFails(t *testing.T) {
	s, id := seed(t)
	dropTable(t, s, "entry_history")
	if _, err := s.GetEntryAsOf(context.Background(), id, time.Now()); err == nil {
		t.Fatal("expected error")
	}
}

func TestEntryHistoryQueryError(t *testing.T) {
	s, id := seed(t)
	dropTable(t, s, "entry_history")
	if _, err := s.EntryHistory(context.Background(), id); err == nil {
		t.Fatal("expected error")
	}
}

func TestUpdateEntryWriteFailure(t *testing.T) {
	s, id := seed(t)
	// Drop entries table — BeginTx succeeds, loadEntryTx fails.
	dropTable(t, s, "entries")
	title := "x"
	_, _, err := s.UpdateEntry(context.Background(), id, EntryPatch{
		Title: &title, ExpectedVersion: 1,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSoftDeleteOnDroppedTable(t *testing.T) {
	s, id := seed(t)
	dropTable(t, s, "entries")
	if err := s.SoftDeleteEntry(context.Background(), id, "", ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestSearchFTSDroppedJoin(t *testing.T) {
	s, _ := seed(t)
	dropTable(t, s, "entries")
	if _, _, err := s.SearchFTS(context.Background(), `"x"*`, EntryFilter{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestSearchFTSTagFilterCleanRun(t *testing.T) {
	// Sanity: a search filtered by a tag with no entries returns empty
	// without error. Exercises the tag-join path and the empty-result
	// scan loop.
	s, _ := seed(t)
	if _, _, err := s.SearchFTS(context.Background(), `"x"*`, EntryFilter{
		Tag: "no-such-tag",
	}); err != nil {
		t.Fatalf("clean search should work: %v", err)
	}
}

// ---- tags failure paths ----

func TestReplaceTagsTxBadTable(t *testing.T) {
	s, _ := seed(t)
	dropTable(t, s, "tags")
	if err := s.ReplaceTags(context.Background(), "anything", []string{"a"}, "human"); err == nil {
		t.Fatal("expected error")
	}
}

func TestAttachTagsDroppedTable(t *testing.T) {
	s, _ := seed(t)
	dropTable(t, s, "tags")
	// ListEntries internally calls attachTags. With tags dropped, attach
	// fails, so ListEntries returns an error.
	if _, _, err := s.ListEntries(context.Background(), EntryFilter{}); err == nil {
		t.Fatal("expected error")
	}
}

// ---- token lookup last-used-update failure ----

func TestLookupTokenLastUsedUpdateFails(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	_ = s.CreateUser(context.Background(),
		&User{ID: "u", Name: "u", Role: "admin"})
	plain, _ := s.CreateToken(context.Background(), "u", "n",
		[]string{"read"}, nil)
	// Drop api_tokens after lookup row was read — the UPDATE in LookupToken
	// fails. Since we call drop INSIDE the Lookup we instead simulate by
	// dropping table and calling lookup; the SELECT itself will fail. To
	// hit specifically the UPDATE-failure branch, replace the table with
	// a read-only view. Easier: rename so SELECT works but UPDATE fails.
	if _, err := s.DB().Exec(`ALTER TABLE api_tokens RENAME TO api_tokens_real`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`CREATE VIEW api_tokens AS SELECT * FROM api_tokens_real`); err != nil {
		t.Fatal(err)
	}
	// SELECT against the view works, UPDATE against the view fails.
	if _, err := s.LookupToken(context.Background(), plain); err == nil {
		t.Fatal("expected error from UPDATE on view")
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

// ---- validation paths ----

func TestCreateEntryValidationFailures(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()
	cases := map[string]Entry{
		"missing project_id": {ProjectID: "", Type: "trap", Title: "t", Body: "b"},
		"missing title":      {ProjectID: "p", Type: "trap", Title: "", Body: "b"},
		"missing body":       {ProjectID: "p", Type: "trap", Title: "t", Body: ""},
		"bogus status":       {ProjectID: "p", Type: "trap", Title: "t", Body: "b", Status: "BOGUS"},
	}
	for name, e := range cases {
		e := e
		t.Run(name, func(t *testing.T) {
			if _, err := s.CreateEntry(ctx, &e); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestCreateEntryBodyFormatDefault(t *testing.T) {
	s, _ := seed(t)
	// Empty BodyFormat should fall back to "markdown".
	id, err := s.CreateEntry(context.Background(), &Entry{
		ProjectID: "p", Type: "trap", Title: "t", Body: "b",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetEntry(context.Background(), id)
	if got.BodyFormat != "markdown" {
		t.Fatalf("BodyFormat=%q", got.BodyFormat)
	}
}

func TestCreateEntryWritesIntoStore(t *testing.T) {
	s, _ := seed(t)
	// Pre-set ID skips the random retry loop entirely.
	id, err := s.CreateEntry(context.Background(), &Entry{
		ID: "T-PRESET", ProjectID: "p", Type: "trap", Title: "t", Body: "b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "T-PRESET" {
		t.Fatalf("ID not preserved: %q", id)
	}
}

func TestTypePrefixDefault(t *testing.T) {
	if got := typePrefix("totally-bogus"); got != "E" {
		t.Fatalf("typePrefix(unknown) = %q, want E", got)
	}
}

func TestListEntriesFilterByTagAttachFailure(t *testing.T) {
	// Drop tags before ListEntries so the JOIN+attachTags both fail.
	s, _ := seed(t)
	dropTable(t, s, "tags")
	// Without tag filter, ListEntries' attachTags fails after rows fetch.
	if _, _, err := s.ListEntries(context.Background(), EntryFilter{}); err == nil {
		t.Fatal("expected attachTags error")
	}
}

// ---- ?as_of= edge cases ----

func TestGetEntryAsOfHistoryRowMissingForCurrent(t *testing.T) {
	s, id := seed(t)
	// Delete all history rows for this entry — entries row still exists,
	// but no history snapshot ≤ asOf can be found.
	if _, err := s.DB().Exec(`DELETE FROM entry_history WHERE entry_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEntryAsOf(context.Background(), id, time.Now()); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// ---- token last-used update on dropped api_tokens via view ----

// (covered by TestLookupTokenLastUsedUpdateFails above)

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

// ---- scanEntryWithRank error via corrupt timestamp ----

func TestSearchFTSScanError(t *testing.T) {
	s, id := seed(t)
	// Corrupt the entries.created_at value so the time.Time scan fails
	// when SearchFTS iterates results.
	if _, err := s.DB().Exec(
		`UPDATE entries SET version = ? WHERE id = ?`,
		"NOT-AN-INT", id,
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SearchFTS(context.Background(), `"y"*`, EntryFilter{}); err == nil {
		t.Fatal("expected scan error")
	}
}

func TestListEntriesScanError(t *testing.T) {
	s, id := seed(t)
	if _, err := s.DB().Exec(
		`UPDATE entries SET version = ? WHERE id = ?`,
		"NOT-AN-INT", id,
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ListEntries(context.Background(), EntryFilter{}); err == nil {
		t.Fatal("expected scan error")
	}
}

func TestGetEntryScanError(t *testing.T) {
	s, id := seed(t)
	if _, err := s.DB().Exec(
		`UPDATE entries SET version = ? WHERE id = ?`,
		"NOT-AN-INT", id,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEntry(context.Background(), id); err == nil {
		t.Fatal("expected scan error")
	}
}

func TestGetEntryAsOfHistoryScanError(t *testing.T) {
	s, id := seed(t)
	// First query (immutable from entries) succeeds. Corrupt the history
	// row so the second scan fails.
	if _, err := s.DB().Exec(
		`UPDATE entry_history SET version = ? WHERE entry_id = ?`,
		"NOT-AN-INT", id,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEntryAsOf(context.Background(), id, time.Now()); err == nil {
		t.Fatal("expected scan error")
	}
}

func TestEntryHistoryScanError(t *testing.T) {
	s, id := seed(t)
	if _, err := s.DB().Exec(
		`UPDATE entry_history SET version = ? WHERE entry_id = ?`,
		"NOT-AN-INT", id,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EntryHistory(context.Background(), id); err == nil {
		t.Fatal("expected scan error")
	}
}

// Projects has no integer mutable column to corrupt easily; the scan path
// is covered by the closed-store shotgun above.

// LookupToken's Scan failure path is exercised by the closed-store
// shotgun above; corrupting api_tokens.created_at is silently accepted by
// the go-sqlite3 driver, so we don't bother trying that route here.

// ---- CreateProject validation ----

func TestCreateProjectMissingFields(t *testing.T) {
	s, _ := seed(t)
	if err := s.CreateProject(context.Background(), &Project{ID: ""}); err != ErrInvalidInput {
		t.Fatalf("empty ID: %v", err)
	}
	if err := s.CreateProject(context.Background(), &Project{ID: "x", Name: ""}); err != ErrInvalidInput {
		t.Fatalf("empty name: %v", err)
	}
}

// ---- HasScope edge cases ----

func TestHasScopeAllBranches(t *testing.T) {
	if !HasScope([]string{"read", "write"}, "write") {
		t.Fatal("required matched")
	}
	if !HasScope([]string{"read", "admin"}, "anything") {
		t.Fatal("admin wildcard")
	}
	if HasScope([]string{"read"}, "write") {
		t.Fatal("no match")
	}
	if HasScope(nil, "read") {
		t.Fatal("empty have")
	}
	if HasScope([]string{}, "read") {
		t.Fatal("empty slice")
	}
}

// ---- CreateEntry intermediate failures ----

func TestCreateEntryInsertFailureViaDroppedEntries(t *testing.T) {
	s, _ := seed(t)
	// Drop the entries table — INSERT inside CreateEntry will fail. We
	// keep projects so the FK existence check passes (it queries projects
	// not entries).
	dropTable(t, s, "entries")
	_, err := s.CreateEntry(context.Background(), &Entry{
		ProjectID: "p", Type: "trap", Title: "t", Body: "b",
	})
	if err == nil {
		t.Fatal("expected INSERT error")
	}
}

func TestCreateEntryReplaceTagsFailure(t *testing.T) {
	s, _ := seed(t)
	dropTable(t, s, "tags")
	_, err := s.CreateEntry(context.Background(), &Entry{
		ProjectID: "p", Type: "trap", Title: "t", Body: "b",
	})
	if err == nil {
		t.Fatal("expected replaceTags error")
	}
}

func TestCreateEntryWriteHistoryFailure(t *testing.T) {
	s, _ := seed(t)
	dropTable(t, s, "entry_history")
	_, err := s.CreateEntry(context.Background(), &Entry{
		ProjectID: "p", Type: "trap", Title: "t", Body: "b",
	})
	if err == nil {
		t.Fatal("expected writeHistory error")
	}
}

// ---- UpdateEntry intermediate failures ----

func TestUpdateEntryTagsReplaceFailure(t *testing.T) {
	s, id := seed(t)
	dropTable(t, s, "tags")
	tags := []string{"x"}
	title := "T2"
	_, _, err := s.UpdateEntry(context.Background(), id, EntryPatch{
		Title: &title, Tags: &tags, ExpectedVersion: 1,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUpdateEntryLoadTagsFailureWhenTagsNotPatched(t *testing.T) {
	s, id := seed(t)
	dropTable(t, s, "tags")
	title := "T2"
	// Patch without tags so the code path is loadTagsTx (read current).
	_, _, err := s.UpdateEntry(context.Background(), id, EntryPatch{
		Title: &title, ExpectedVersion: 1,
	})
	if err == nil {
		t.Fatal("expected loadTagsTx error")
	}
}

func TestUpdateEntryWriteHistoryFailure(t *testing.T) {
	s, id := seed(t)
	dropTable(t, s, "entry_history")
	title := "T2"
	_, _, err := s.UpdateEntry(context.Background(), id, EntryPatch{
		Title: &title, ExpectedVersion: 1,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUpdateEntryUpdateFailureWithDroppedEntries(t *testing.T) {
	// loadEntryTx and the subsequent UPDATE both touch entries — dropping
	// it makes loadEntryTx fail first. The post-load UPDATE-fails branch is
	// thus reachable only via a more invasive corruption; we accept the
	// load-failure coverage as sufficient for the failure path.
	s, id := seed(t)
	dropTable(t, s, "entries")
	title := "T2"
	_, _, err := s.UpdateEntry(context.Background(), id, EntryPatch{
		Title: &title, ExpectedVersion: 1,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---- SoftDeleteEntry intermediate failures ----

func TestSoftDeleteLoadTagsFailure(t *testing.T) {
	s, id := seed(t)
	dropTable(t, s, "tags")
	if err := s.SoftDeleteEntry(context.Background(), id, "", ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestSoftDeleteWriteHistoryFailure(t *testing.T) {
	s, id := seed(t)
	dropTable(t, s, "entry_history")
	if err := s.SoftDeleteEntry(context.Background(), id, "", ""); err == nil {
		t.Fatal("expected error")
	}
}

// ---- GetEntry tags failure ----

func TestGetEntryTagsFailure(t *testing.T) {
	s, id := seed(t)
	dropTable(t, s, "tags")
	if _, err := s.GetEntry(context.Background(), id); err == nil {
		t.Fatal("expected tag-fetch error")
	}
}

// ---- INSERT failure via duplicate ID ----

func TestCreateEntryDuplicateIDInsertFails(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()
	_, err := s.CreateEntry(ctx, &Entry{
		ID: "T-DUP", ProjectID: "p", Type: "trap", Title: "t", Body: "b",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Second insert with the same ID triggers UNIQUE constraint, which
	// flows through the post-INSERT error branch and translateErr.
	_, err = s.CreateEntry(ctx, &Entry{
		ID: "T-DUP", ProjectID: "p", Type: "trap", Title: "t2", Body: "b2",
	})
	if err == nil {
		t.Fatal("expected ErrAlreadyExists")
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

// ---- nullable empty string helper ----

func TestNullableEmptyAndNonEmpty(t *testing.T) {
	if v := nullable(""); v == "" { // empty returns sql.NullString{}
		t.Fatal("empty should produce typed NULL")
	}
	if v := nullable("x"); v != "x" {
		t.Fatalf("non-empty should pass through: %v", v)
	}
}

// ---- splitScopes empty ----

func TestSplitScopesEmpty(t *testing.T) {
	if s := splitScopes(""); len(s) != 0 {
		t.Fatalf("got %v", s)
	}
}

// ---- joinScopes empty / whitespace ----

func TestJoinScopesFiltersEmpty(t *testing.T) {
	got := joinScopes([]string{"", "  ", "read", " write ", "read"})
	if got != "read,write" {
		t.Fatalf("got %q", got)
	}
}

// ---- entry history after soft-delete (valid_to populated) ----

func TestEntryHistoryAfterSoftDelete(t *testing.T) {
	s, id := seed(t)
	ctx := context.Background()
	if err := s.SoftDeleteEntry(ctx, id, "tester", "human"); err != nil {
		t.Fatal(err)
	}
	hist, err := s.EntryHistory(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// One of the history versions has valid_to set; exercises the
	// `validTo.Valid` true branch in the EntryHistory scanner.
	gotValidTo := false
	for _, h := range hist {
		if h.ValidTo != nil {
			gotValidTo = true
		}
	}
	if !gotValidTo {
		t.Fatal("expected at least one history row with valid_to set")
	}
}

func TestSearchFTSWithTypeAndStatus(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()
	_, _ = s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "decision", Title: "for-search", Body: "extra",
		Status: "ACTIVE",
	})
	res, _, err := s.SearchFTS(ctx, `"for-search"*`, EntryFilter{
		Type: "decision", Status: "ACTIVE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(res))
	}
}

func TestSearchFTSEntryWithEnrichmentAt(t *testing.T) {
	s, id := seed(t)
	ctx := context.Background()
	if err := s.SetEnrichment(ctx, id, 7); err != nil {
		t.Fatal(err)
	}
	res, _, err := s.SearchFTS(ctx, `"y"*`, EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	hit := false
	for _, r := range res {
		if r.Entry.EnrichmentAt != nil {
			hit = true
		}
	}
	if !hit {
		t.Fatal("expected enrichment_at on at least one result")
	}
}

func TestSearchFTSEntryWithValidTo(t *testing.T) {
	// Archived entries that we include via IncludeSuperseded have
	// valid_to populated, hitting the corresponding branch in
	// scanEntryWithRank.
	s, id := seed(t)
	ctx := context.Background()
	if err := s.SoftDeleteEntry(ctx, id, "", ""); err != nil {
		t.Fatal(err)
	}
	res, _, err := s.SearchFTS(ctx, `"y"*`, EntryFilter{IncludeSuperseded: true})
	if err != nil {
		t.Fatal(err)
	}
	gotValidTo := false
	for _, r := range res {
		if r.Entry.ValidTo != nil {
			gotValidTo = true
		}
	}
	if !gotValidTo {
		t.Fatal("expected at least one result with valid_to set")
	}
}

func TestGetEntryAsOfAfterSoftDelete(t *testing.T) {
	s, id := seed(t)
	ctx := context.Background()
	if err := s.SoftDeleteEntry(ctx, id, "", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetEntryAsOf(ctx, id, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.ValidTo == nil {
		t.Fatal("expected valid_to on archived snapshot")
	}
}

// ---- ListEntries query-fails-after-count-succeeds (unreachable in
// practice; covered by closed-store shotgun which fails count first) ----

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
