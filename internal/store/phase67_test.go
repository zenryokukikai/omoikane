package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEntryTiers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	e1, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "tier1 candidate", Body: "x", Status: "ACTIVE",
	})
	// 3 helpful cases → tier 1 candidate
	for i := 0; i < 3; i++ {
		cid, _ := s.CreateCase(ctx, &UsageCase{EntryID: e1})
		r := "helpful"
		_, _ = s.PatchCase(ctx, cid, CasePatch{Result: &r})
	}

	e2, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "tier4 candidate", Body: "x", Status: "ACTIVE",
	})
	for i := 0; i < 3; i++ {
		cid, _ := s.CreateCase(ctx, &UsageCase{EntryID: e2})
		r := "misleading"
		_, _ = s.PatchCase(ctx, cid, CasePatch{Result: &r})
	}

	// Tier 3: no signal
	_, _ = s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "tier3", Body: "x", Status: "ACTIVE",
	})

	rows1, err := s.ListEntriesByTier(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows1) != 1 || rows1[0].ID != e1 {
		t.Fatalf("tier1: %+v", rows1)
	}
	rows4, _ := s.ListEntriesByTier(ctx, 4, 10)
	if len(rows4) != 1 || rows4[0].ID != e2 {
		t.Fatalf("tier4: %+v", rows4)
	}
	rows3, _ := s.ListEntriesByTier(ctx, 3, 10)
	if len(rows3) < 1 {
		t.Fatalf("tier3: %+v", rows3)
	}

	if _, err := s.ListEntriesByTier(ctx, 99, 10); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid tier: %v", err)
	}
}

func TestCoordinatorAnomalyScan(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	e, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "x", Body: "y", Status: "DRAFT",
	})
	// Push misleading count high
	for i := 0; i < 3; i++ {
		cid, _ := s.CreateCase(ctx, &UsageCase{EntryID: e})
		r := "misleading"
		_, _ = s.PatchCase(ctx, cid, CasePatch{Result: &r})
	}
	// Register an instance with no heartbeat.
	if _, err := s.RegisterLibrarianInstance(ctx, &LibrarianInstance{
		Role: "detective",
	}); err != nil {
		t.Fatal(err)
	}

	out, err := s.CoordinatorAnomalyScan(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out.ReviewQueueDepth < 1 {
		t.Fatalf("review-depth: %d", out.ReviewQueueDepth)
	}
	if len(out.MisleadingHeavy) != 1 || out.MisleadingHeavy[0] != e {
		t.Fatalf("misleading: %+v", out.MisleadingHeavy)
	}
	if len(out.StaleInstances) != 1 {
		t.Fatalf("stale: %+v", out.StaleInstances)
	}
}

func TestProposeQuartet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	q, err := s.ProposeQuartet(ctx, "supersede T-X", "")
	if err != nil {
		t.Fatal(err)
	}
	if q.Judge != "judge-01" || q.Participant1 == "" {
		t.Fatalf("quartet: %+v", q)
	}
}

func TestRunBackup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	target := filepath.Join(dir, "snap.db")
	job, err := s.RunBackup(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "DONE" || job.Bytes <= 0 {
		t.Fatalf("job: %+v", job)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("snapshot file: %v", err)
	}

	// List + Get
	list, err := s.ListBackups(ctx, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v err=%v", list, err)
	}
	got, err := s.GetBackup(ctx, job.ID)
	if err != nil || got.Path != target {
		t.Fatalf("get: %+v err=%v", got, err)
	}
}

func TestRunBackupValidation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.RunBackup(ctx, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty path: %v", err)
	}
	if _, err := s.RunBackup(ctx, "/tmp/x';drop"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("dangerous path: %v", err)
	}
	// VACUUM INTO into an existing file fails — exercises the
	// FAILED-status update path.
	dir := t.TempDir()
	existing := filepath.Join(dir, "exists.db")
	_ = os.WriteFile(existing, []byte("not empty"), 0o644)
	if _, err := s.RunBackup(ctx, existing); err == nil {
		t.Fatal("expected vacuum error on existing file")
	}
	// The job row should be marked FAILED.
	list, _ := s.ListBackups(ctx, 10)
	found := false
	for _, j := range list {
		if j.Path == existing && j.Status == "FAILED" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a FAILED job: %+v", list)
	}
}

func TestArchiveDormant(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	id, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "old", Body: "x", Status: "ACTIVE",
	})
	// Backdate updated_at past the dormancy threshold.
	if _, err := s.DB().Exec(`UPDATE entries SET updated_at = datetime('now','-200 days') WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	n, err := s.ArchiveDormantEntries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 archived, got %d", n)
	}
	got, _ := s.GetEntry(ctx, id)
	if got.Status != "ARCHIVED" {
		t.Fatalf("status=%s", got.Status)
	}

	// Idempotent: re-run is a no-op (dormant_entries filter excludes
	// non-ACTIVE).
	n2, _ := s.ArchiveDormantEntries(ctx)
	if n2 != 0 {
		t.Fatalf("re-run archived: %d", n2)
	}
}

func TestLLMUsage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.RecordLLMUsage(ctx, &LLMUsage{
		Provider: "anthropic", Model: "claude-x", InputTokens: 100, OutputTokens: 50, CostUSD: 0.01,
		Purpose: "enrichment",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLLMUsage(ctx, &LLMUsage{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing provider: %v", err)
	}

	stats, err := s.LLMUsageStatsWindow(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Calls != 1 || stats.InputTokens != 100 || stats.WindowDays != 30 {
		t.Fatalf("stats: %+v", stats)
	}
}

func TestHealthCoverageStats(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	e, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "x", Body: "y", Status: "ACTIVE",
		Tags: []string{"foo"},
	})
	// Add a case
	_, _ = s.CreateCase(ctx, &UsageCase{EntryID: e})
	// Add a relation
	e2, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "z", Body: "y", Status: "ACTIVE",
	})
	_ = s.CreateRelation(ctx, &Relation{FromID: e, ToID: e2, RelType: "related"})
	// Attach to a hierarchy node
	h, _ := s.CreateHierarchyNode(ctx, &HierarchyNode{Name: "n"})
	_ = s.AttachEntryToNode(ctx, h, e, 1.0, "t")

	stats, err := s.HealthCoverageStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalActive != 2 || stats.WithTags < 1 || stats.WithFeedback < 1 ||
		stats.WithRelations < 1 || stats.WithHierarchy < 1 {
		t.Fatalf("stats: %+v", stats)
	}
	// Make stats not entirely uniform.
	_ = time.Now
}
