package store

import (
	"context"
	"testing"
)

// All of the Phase 3 SQL paths share a tight pattern: open transaction or
// run a single statement, scan rows, return. We exercise the otherwise-
// unreachable error branches by dropping the underlying tables before the
// call. This mirrors the Phase 1/2 fault_test.go pattern.

func TestCreateCaseFKFailure(t *testing.T) {
	s, _ := seed(t)
	if _, err := s.CreateCase(context.Background(), &UsageCase{EntryID: "missing"}); err == nil {
		t.Fatal("expected FK failure")
	}
}

func TestGetCaseNotFound(t *testing.T) {
	s, _ := seed(t)
	if _, err := s.GetCase(context.Background(), "missing"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestListCasesTableMissing(t *testing.T) {
	s, _ := seed(t)
	dropTable(t, s, "usage_cases")
	if _, err := s.ListCases(context.Background(), "x", 0); err == nil {
		t.Fatal("expected error")
	}
}

func TestHelpfulnessScoresTableMissing(t *testing.T) {
	s, _ := seed(t)
	dropTable(t, s, "usage_cases")
	if _, err := s.HelpfulnessScores(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestReviewQueueTableMissing(t *testing.T) {
	s, _ := seed(t)
	// Drop the underlying view source so the view returns an error.
	if _, err := s.DB().Exec(`DROP VIEW review_queue`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewQueue(context.Background(), 0); err == nil {
		t.Fatal("expected error")
	}
}

func TestEntrySignalViewMissing(t *testing.T) {
	s, id := seed(t)
	if _, err := s.DB().Exec(`DROP VIEW entry_signals`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EntrySignal(context.Background(), id); err == nil {
		t.Fatal("expected error")
	}
}

func TestCreateRelationFKFailure(t *testing.T) {
	s, _ := seed(t)
	if err := s.CreateRelation(context.Background(), &Relation{
		FromID: "ghost", ToID: "phantom", RelType: "related",
	}); err == nil {
		t.Fatal("expected FK failure")
	}
}

func TestListRelationsTableMissing(t *testing.T) {
	s, _ := seed(t)
	dropTable(t, s, "relations")
	if _, err := s.ListRelationsFrom(context.Background(), "x"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := s.ListRelationsTo(context.Background(), "x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestSituationsTableMissing(t *testing.T) {
	s, _ := seed(t)
	dropTable(t, s, "situations")
	ctx := context.Background()
	if _, err := s.CreateSituation(ctx, &Situation{Description: "x"}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := s.GetSituation(ctx, "missing"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := s.ListSituations(ctx, "", 0); err == nil {
		t.Fatal("expected error")
	}
	if err := s.DeleteSituation(ctx, "x"); err == nil {
		t.Fatal("expected error")
	}
	// Drop the FTS table to force LookupBySituation's query path to error.
	dropTable(t, s, "situations_fts")
	if _, err := s.LookupBySituation(ctx, "rectangular artifact", 5); err == nil {
		t.Fatal("expected error")
	}
	// Drop the link table for the entry-linking helpers.
	dropTable(t, s, "situation_entries")
	if err := s.LinkEntryToSituation(ctx, "x", "y", 1, ""); err == nil {
		t.Fatal("expected error")
	}
	if err := s.UnlinkEntryFromSituation(ctx, "x", "y"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := s.ListSituationEntries(ctx, "x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestClustersTableMissing(t *testing.T) {
	s, _ := seed(t)
	dropTable(t, s, "incident_clusters")
	ctx := context.Background()
	if _, err := s.CreateCluster(ctx, &IncidentCluster{Title: "t"}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := s.GetCluster(ctx, "missing"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := s.ListClusters(ctx, "", "", 0); err == nil {
		t.Fatal("expected error")
	}
	if err := s.AddClusterMember(ctx, "x", "y", 1, ""); err == nil {
		t.Fatal("expected error")
	}
	if err := s.PromoteCluster(ctx, "x", "y"); err == nil {
		t.Fatal("expected error")
	}
	if err := s.DismissCluster(ctx, "x"); err == nil {
		t.Fatal("expected error")
	}
	// ListClusterMembers + RemoveClusterMember go through
	// incident_cluster_members which is FK-linked but still present; drop
	// it to surface the SQL error.
	dropTable(t, s, "incident_cluster_members")
	if err := s.RemoveClusterMember(ctx, "x", "y"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := s.ListClusterMembers(ctx, "x"); err == nil {
		t.Fatal("expected error")
	}
	// BuildIncidentClusters with no incidents returns (0,0,nil) — drop
	// the underlying entries table to force the QueryContext error path.
	dropTable(t, s, "entries")
	if _, _, err := s.BuildIncidentClusters(ctx, "p", 0.3, 2); err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildIncidentClustersLoadMembershipFailure(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()
	// Seed a couple incidents so we get past the len<minMembers short-circuit.
	for _, sym := range []string{
		"rectangular mask leak training",
		"rectangular mask drift training",
	} {
		if _, err := s.CreateEntry(ctx, &Entry{
			ProjectID: "p", Type: "incident", Title: "x",
			Body: "b", Symptom: sym, Status: "ACTIVE",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Now drop the cluster_members table to break loadExistingClusterMemberships.
	dropTable(t, s, "incident_cluster_members")
	if _, _, err := s.BuildIncidentClusters(ctx, "p", 0.3, 2); err == nil {
		t.Fatal("expected error")
	}
}
