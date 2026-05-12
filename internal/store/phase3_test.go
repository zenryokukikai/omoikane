package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func phase3Seed(t *testing.T) (*Store, context.Context, []string) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, &Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 4)
	for i, title := range []string{"alpha", "beta", "gamma", "delta"} {
		id, err := s.CreateEntry(ctx, &Entry{
			ProjectID: "p", Type: "trap", Title: title,
			Body:    "body" + title,
			Symptom: "rectangular artifact at inference",
		})
		if err != nil {
			t.Fatalf("create entry %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	return s, ctx, ids
}

// ============================================================
// usage_cases
// ============================================================

func TestCreateCaseAndPatch(t *testing.T) {
	s, ctx, ids := phase3Seed(t)
	caseID, err := s.CreateCase(ctx, &UsageCase{
		EntryID:      ids[0],
		ProjectID:    "p",
		TriggerQuery: "mask leak",
		AgentRole:    "human",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if caseID == "" {
		t.Fatal("empty case_id")
	}
	got, err := s.GetCase(ctx, caseID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.EntryID != ids[0] {
		t.Fatalf("entry_id=%s", got.EntryID)
	}

	// patch outcome+result, judged_at must be stamped.
	outcome := "applied"
	result := "helpful"
	patched, err := s.PatchCase(ctx, caseID, CasePatch{Outcome: &outcome, Result: &result})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if patched.Outcome != "applied" || patched.Result != "helpful" {
		t.Fatalf("patch values: %+v", patched)
	}
	if patched.ResultJudgedAt == nil {
		t.Fatal("expected judged_at to be auto-stamped")
	}
	// Second patch with same result should NOT re-stamp judged_at.
	first := *patched.ResultJudgedAt
	notes := "noted"
	patched2, err := s.PatchCase(ctx, caseID, CasePatch{Notes: &notes})
	if err != nil {
		t.Fatalf("patch2: %v", err)
	}
	if patched2.ResultJudgedAt == nil || !patched2.ResultJudgedAt.Equal(first) {
		t.Fatalf("judged_at changed unexpectedly")
	}

	// PatchCase with empty patch returns existing.
	if _, err := s.PatchCase(ctx, caseID, CasePatch{}); err != nil {
		t.Fatalf("empty patch: %v", err)
	}
}

func TestCreateCaseRequiresEntryID(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateCase(context.Background(), &UsageCase{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

func TestPatchCaseNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.PatchCase(context.Background(), "nope", CasePatch{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestListCases(t *testing.T) {
	s, ctx, ids := phase3Seed(t)
	for i := 0; i < 3; i++ {
		_, err := s.CreateCase(ctx, &UsageCase{EntryID: ids[0]})
		if err != nil {
			t.Fatal(err)
		}
	}
	cases, err := s.ListCases(ctx, ids[0], 0) // default limit
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 3 {
		t.Fatalf("len=%d", len(cases))
	}
}

func TestEntrySignalAndReviewQueue(t *testing.T) {
	s, ctx, ids := phase3Seed(t)
	// 4 helpful, 4 misleading → flagged for review.
	for i := 0; i < 4; i++ {
		cid, _ := s.CreateCase(ctx, &UsageCase{EntryID: ids[1]})
		result := "helpful"
		_, _ = s.PatchCase(ctx, cid, CasePatch{Result: &result})
	}
	for i := 0; i < 4; i++ {
		cid, _ := s.CreateCase(ctx, &UsageCase{EntryID: ids[1]})
		result := "misleading"
		_, _ = s.PatchCase(ctx, cid, CasePatch{Result: &result})
	}
	sig, err := s.EntrySignal(ctx, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if sig.TotalUses != 8 || sig.HelpfulCount != 4 || sig.MisleadingCount != 4 {
		t.Fatalf("signals: %+v", sig)
	}
	if sig.HelpfulnessScore == nil {
		t.Fatal("expected non-nil helpfulness")
	}

	// HelpfulnessScores bulk.
	bulk, err := s.HelpfulnessScores(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bulk[ids[1]]; !ok {
		t.Fatal("ids[1] not in bulk result")
	}
	// Empty slice → empty map, no error.
	if m, _ := s.HelpfulnessScores(ctx, nil); len(m) != 0 {
		t.Fatalf("expected empty, got %v", m)
	}

	queue, err := s.ReviewQueue(ctx, 0) // default limit
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, q := range queue {
		if q.ID == ids[1] {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ids[1] not in review queue; queue=%+v", queue)
	}
}

func TestEntrySignalNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.EntrySignal(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// ============================================================
// relations + auto-supersede
// ============================================================

func TestValidRelType(t *testing.T) {
	for _, ok := range []string{"related", "supersedes", "conflicts_with",
		"depends_on", "see_also", "duplicate_of", "resolved_by"} {
		if !ValidRelType(ok) {
			t.Fatalf("expected %s valid", ok)
		}
	}
	if ValidRelType("frobnicates") {
		t.Fatal("frobnicates should be invalid")
	}
}

func TestCreateAndDeleteRelation(t *testing.T) {
	s, ctx, ids := phase3Seed(t)
	if err := s.CreateRelation(ctx, &Relation{
		FromID: ids[0], ToID: ids[1], RelType: "related",
	}); err != nil {
		t.Fatal(err)
	}
	// Idempotent upsert.
	if err := s.CreateRelation(ctx, &Relation{
		FromID: ids[0], ToID: ids[1], RelType: "related", Notes: "updated",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := s.ListRelationsFrom(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Notes != "updated" {
		t.Fatalf("relations: %+v", out)
	}
	in, err := s.ListRelationsTo(ctx, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 1 {
		t.Fatalf("incoming: %+v", in)
	}

	if err := s.DeleteRelation(ctx, ids[0], ids[1], "related"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRelation(ctx, ids[0], ids[1], "related"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRelationInvalidType(t *testing.T) {
	s, ctx, ids := phase3Seed(t)
	if err := s.CreateRelation(ctx, &Relation{
		FromID: ids[0], ToID: ids[1], RelType: "nope",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	if err := s.CreateRelation(ctx, &Relation{
		FromID: ids[0], ToID: ids[0], RelType: "related",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("self-relate must fail, got %v", err)
	}
}

func TestAutoSupersedeOnConflict(t *testing.T) {
	s, ctx, ids := phase3Seed(t)
	// Make sure created_at order is deterministic: ids[0] was inserted first.
	if err := s.CreateRelation(ctx, &Relation{
		FromID: ids[0], ToID: ids[1], RelType: "conflicts_with",
	}); err != nil {
		t.Fatal(err)
	}
	older, err := s.GetEntry(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	newer, err := s.GetEntry(ctx, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if older.Status != "SUPERSEDED" {
		t.Fatalf("older.Status=%q", older.Status)
	}
	if older.SupersededBy != ids[1] {
		t.Fatalf("superseded_by=%q", older.SupersededBy)
	}
	if newer.Status == "SUPERSEDED" {
		t.Fatalf("newer should not be superseded")
	}

	// Idempotent: re-create conflict, no further status change.
	if err := s.CreateRelation(ctx, &Relation{
		FromID: ids[0], ToID: ids[1], RelType: "conflicts_with",
	}); err != nil {
		t.Fatal(err)
	}
	older2, _ := s.GetEntry(ctx, ids[0])
	if older2.Status != "SUPERSEDED" || older2.Version != older.Version {
		t.Fatalf("idempotent failure: %+v", older2)
	}
}

func TestExplicitSupersedes(t *testing.T) {
	s, ctx, ids := phase3Seed(t)
	if err := s.CreateRelation(ctx, &Relation{
		FromID: ids[1], ToID: ids[0], RelType: "supersedes",
	}); err != nil {
		t.Fatal(err)
	}
	loser, _ := s.GetEntry(ctx, ids[0])
	if loser.Status != "SUPERSEDED" {
		t.Fatalf("expected SUPERSEDED, got %q", loser.Status)
	}
	if loser.SupersededBy != ids[1] {
		t.Fatalf("superseded_by=%q", loser.SupersededBy)
	}
}

// ============================================================
// situations
// ============================================================

func TestSituationsCRUDAndLookup(t *testing.T) {
	s, ctx, ids := phase3Seed(t)
	id, err := s.CreateSituation(ctx, &Situation{
		ProjectID:   "p",
		Description: "rectangular mask training pipeline crash",
		Domain:      "training",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("empty id")
	}
	if err := s.LinkEntryToSituation(ctx, id, ids[0], 0.9, "primary"); err != nil {
		t.Fatal(err)
	}
	// Idempotent re-link.
	if err := s.LinkEntryToSituation(ctx, id, ids[0], 1.0, "refreshed"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSituation(ctx, id)
	if err != nil || got.Domain != "training" {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	sits, err := s.ListSituations(ctx, "p", 0)
	if err != nil || len(sits) != 1 {
		t.Fatalf("list: %+v err=%v", sits, err)
	}
	links, err := s.ListSituationEntries(ctx, id)
	if err != nil || len(links) != 1 || links[0].EntryID != ids[0] {
		t.Fatalf("links: %+v err=%v", links, err)
	}

	hits, err := s.LookupBySituation(ctx, "rectangular mask training", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].EntryID != ids[0] {
		t.Fatalf("lookup: %+v", hits)
	}

	if err := s.UnlinkEntryFromSituation(ctx, id, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.UnlinkEntryFromSituation(ctx, id, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	if err := s.DeleteSituation(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSituation(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSituationsInvalid(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.CreateSituation(ctx, &Situation{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if _, err := s.LookupBySituation(ctx, "", 5); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty lookup: %v", err)
	}
	if hits, err := s.LookupBySituation(ctx, "x", 5); err != nil || hits != nil {
		t.Fatalf("short query: hits=%v err=%v", hits, err)
	}
}

// ============================================================
// clusters
// ============================================================

func TestClusterCRUDAndPromote(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id, err := s.CreateCluster(ctx, &IncidentCluster{
		Title:   "mask training crashes",
		Summary: "recurring failures",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("empty id")
	}

	// project + members
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	e1, _ := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "incident", Title: "a", Body: "x"})
	e2, _ := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "incident", Title: "b", Body: "y"})
	if err := s.AddClusterMember(ctx, id, e1, 0.9, "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddClusterMember(ctx, id, e2, 0.8, "test"); err != nil {
		t.Fatal(err)
	}
	c, err := s.GetCluster(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.MemberCount != 2 {
		t.Fatalf("member_count=%d", c.MemberCount)
	}

	members, err := s.ListClusterMembers(ctx, id)
	if err != nil || len(members) != 2 {
		t.Fatalf("members: %+v err=%v", members, err)
	}

	if err := s.RemoveClusterMember(ctx, id, e2); err != nil {
		t.Fatal(err)
	}
	c2, _ := s.GetCluster(ctx, id)
	if c2.MemberCount != 1 {
		t.Fatalf("post-remove count=%d", c2.MemberCount)
	}
	if err := s.RemoveClusterMember(ctx, id, e2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double-remove: %v", err)
	}

	// Promote
	if err := s.PromoteCluster(ctx, id, e1); err != nil {
		t.Fatal(err)
	}
	cp, _ := s.GetCluster(ctx, id)
	if cp.Status != "PROMOTED" || cp.PromotedToEntryID != e1 {
		t.Fatalf("promote: %+v", cp)
	}
	if err := s.PromoteCluster(ctx, "nope", e1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("promote-missing: %v", err)
	}

	// Dismiss requires a fresh cluster (already PROMOTED is updated, no
	// status restriction; the contract is "DISMISSED wins over OPEN").
	id2, _ := s.CreateCluster(ctx, &IncidentCluster{Title: "dummy"})
	if err := s.DismissCluster(ctx, id2); err != nil {
		t.Fatal(err)
	}
	if err := s.DismissCluster(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dismiss-missing: %v", err)
	}

	// list filters
	cls, err := s.ListClusters(ctx, "", "PROMOTED", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cls) != 1 || cls[0].ID != id {
		t.Fatalf("list-PROMOTED: %+v", cls)
	}
}

func TestClusterInvalid(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateCluster(context.Background(), &IncidentCluster{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing title: %v", err)
	}
}

func TestBuildIncidentClusters(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	// 3 incidents with overlapping symptom tokens.
	for _, sym := range []string{
		"rectangular mask leak in training",
		"rectangular mask drift during training",
		"rectangular mask edge artifact training inference",
	} {
		if _, err := s.CreateEntry(ctx, &Entry{
			ProjectID: "p", Type: "incident", Title: "x",
			Body: "b", Symptom: sym, Status: "ACTIVE",
		}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // distinct created_at
	}
	// Unrelated outlier
	if _, err := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "incident", Title: "y",
		Body: "b", Symptom: "TCP connection timeout in CI", Status: "ACTIVE",
	}); err != nil {
		t.Fatal(err)
	}

	created, added, err := s.BuildIncidentClusters(ctx, "p", 0.3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 || added < 2 {
		t.Fatalf("created=%d added=%d", created, added)
	}

	// Second pass — taken entries are skipped, so no new cluster.
	created2, _, err := s.BuildIncidentClusters(ctx, "p", 0.3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if created2 != 0 {
		t.Fatalf("second pass created=%d", created2)
	}

	// Defaults exercised (threshold <= 0, minMembers < 2).
	if _, _, err := s.BuildIncidentClusters(ctx, "p", 0, 0); err != nil {
		t.Fatal(err)
	}
}

func TestBuildIncidentClustersEmpty(t *testing.T) {
	s := newTestStore(t)
	created, added, err := s.BuildIncidentClusters(context.Background(), "", 0.4, 2)
	if err != nil || created != 0 || added != 0 {
		t.Fatalf("empty: created=%d added=%d err=%v", created, added, err)
	}
}

// ============================================================
// helpers
// ============================================================

func TestNullFloatScan(t *testing.T) {
	cases := []struct {
		in    any
		valid bool
		val   float64
	}{
		{nil, false, 0},
		{float64(1.5), true, 1.5},
		{int64(3), true, 3.0},
		{[]byte("2.5"), true, 2.5},
		{"4.25", true, 4.25},
	}
	for _, c := range cases {
		var n nullFloat
		if err := n.Scan(c.in); err != nil {
			t.Fatalf("scan(%v): %v", c.in, err)
		}
		if n.Valid != c.valid || n.Value != c.val {
			t.Fatalf("scan(%v): %+v", c.in, n)
		}
	}
	// bad string
	var n nullFloat
	if err := n.Scan([]byte("bogus")); err == nil {
		t.Fatal("expected scan error")
	}
	if err := n.Scan("bogus"); err == nil {
		t.Fatal("expected scan error on string")
	}
}

func TestJaccardAndTokens(t *testing.T) {
	if jaccard(nil, nil) != 0 {
		t.Fatal("empty jaccard")
	}
	a := symptomTokens("hello world foo")
	b := symptomTokens("hello world bar")
	if got := jaccard(a, b); got < 0.4 || got > 0.6 {
		t.Fatalf("jaccard hello/world overlap: %f", got)
	}
	if got := jaccard(a, map[string]struct{}{}); got != 0 {
		t.Fatalf("empty rhs: %f", got)
	}
}
