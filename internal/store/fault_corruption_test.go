package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Fault aspect: schema/data corruption injected through SQL — dropped
// tables, a table swapped for a read-only view, corrupt column values, and
// deleted history rows. The harness (seed, dropTable, dropAfterSeed) lives
// in fault_test.go.

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
// is covered by the closed-store shotgun in fault_test.go.

// LookupToken's Scan failure path is exercised by the closed-store
// shotgun in fault_test.go; corrupting api_tokens.created_at is silently
// accepted by the go-sqlite3 driver, so we don't bother trying that route
// here.

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
