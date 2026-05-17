package store

import (
	"context"
	"errors"
	"testing"
)

// seedEntryForFeedback creates a minimal entry suitable as a feedback
// target and returns its id.
func seedEntryForFeedback(t *testing.T, s *Store) string {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateProject(ctx, &Project{ID: "fb", Name: "FB"}); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateEntry(ctx, &Entry{
		ProjectID: "fb", Type: "trap",
		Title: "feedback target", Body: "x", Status: "ACTIVE",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRecordFeedbackHappyPath(t *testing.T) {
	s := newTestStore(t)
	id := seedEntryForFeedback(t, s)
	ctx := context.Background()
	fb := &EntryFeedback{
		EntryID: id, UserID: "u1",
		Signal:  FeedbackSignalHelpful,
		Context: "applied to fix the lower-teeth issue",
	}
	if err := s.RecordFeedback(ctx, fb); err != nil {
		t.Fatalf("record: %v", err)
	}
	if fb.ID == 0 {
		t.Fatal("ID not set")
	}
	if fb.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not populated")
	}
	// Engagement view should reflect the helpful signal.
	eng, err := s.GetEngagement(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if eng.FeedbackHelpful != 1 {
		t.Errorf("helpful count: %d", eng.FeedbackHelpful)
	}
	// engagement_score with one helpful (+1.0) and smoothing denom (3+1=4)
	// should be ~ 0.25.
	if eng.EngagementScore < 0.2 || eng.EngagementScore > 0.3 {
		t.Errorf("engagement_score: %v (expected ~0.25)", eng.EngagementScore)
	}
}

func TestRecordFeedbackRejectsBadSignal(t *testing.T) {
	s := newTestStore(t)
	id := seedEntryForFeedback(t, s)
	err := s.RecordFeedback(context.Background(), &EntryFeedback{
		EntryID: id, Signal: "AWESOME",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

func TestRecordFeedbackRejectsBlankEntryID(t *testing.T) {
	s := newTestStore(t)
	err := s.RecordFeedback(context.Background(), &EntryFeedback{
		EntryID: "", Signal: FeedbackSignalHelpful,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

func TestRecordFeedbackRejectsMissingEntry(t *testing.T) {
	s := newTestStore(t)
	err := s.RecordFeedback(context.Background(), &EntryFeedback{
		EntryID: "L-NONEXISTENT", Signal: FeedbackSignalHelpful,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// Multiple feedback rows on the same entry from the same user are
// allowed — "helpful" today, "outdated" later is a real flow. Both rows
// must persist.
func TestRecordFeedbackAllowsMultiplePerUser(t *testing.T) {
	s := newTestStore(t)
	id := seedEntryForFeedback(t, s)
	ctx := context.Background()
	for _, sig := range []string{FeedbackSignalHelpful, FeedbackSignalOutdated} {
		if err := s.RecordFeedback(ctx, &EntryFeedback{
			EntryID: id, UserID: "u1", Signal: sig,
		}); err != nil {
			t.Fatalf("%s: %v", sig, err)
		}
	}
	eng, _ := s.GetEngagement(ctx, id)
	if eng.FeedbackHelpful != 1 {
		t.Errorf("helpful: %d", eng.FeedbackHelpful)
	}
	if eng.FeedbackOutdated != 1 {
		t.Errorf("outdated: %d", eng.FeedbackOutdated)
	}
}

// engagement_score with one helpful (+1.0) and one wrong (-1.0):
//   numerator   = 1.0 + (-1.0) = 0
//   denominator = 3 + 2 = 5
//   score       = 0
// Effectively neutralized — opposing signals cancel. This is the right
// behavior: a controversial entry should have low score, not high
// "we have lots of feedback so it must be great" score.
func TestEngagementScoreCancelsOpposingSignals(t *testing.T) {
	s := newTestStore(t)
	id := seedEntryForFeedback(t, s)
	ctx := context.Background()
	_ = s.RecordFeedback(ctx, &EntryFeedback{EntryID: id, Signal: FeedbackSignalHelpful})
	_ = s.RecordFeedback(ctx, &EntryFeedback{EntryID: id, Signal: FeedbackSignalWrong})
	eng, _ := s.GetEngagement(ctx, id)
	if eng.EngagementScore < -0.05 || eng.EngagementScore > 0.05 {
		t.Errorf("score should be near 0 for opposing signals, got %v", eng.EngagementScore)
	}
}

func TestGetEngagementMissingEntry(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetEngagement(context.Background(), "L-MISSING")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestFeedbackSignalsExposed(t *testing.T) {
	got := FeedbackSignals()
	if len(got) != 6 {
		t.Fatalf("expected 6 signals, got %d: %v", len(got), got)
	}
}
