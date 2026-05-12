package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func phase5Seed(t *testing.T) (*Store, context.Context) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	return s, ctx
}

func TestValidLibrarianRole(t *testing.T) {
	for _, r := range []string{"coordinator", "cataloger", "curator",
		"detective", "conservator", "scout", "summarizer", "judge"} {
		if !ValidLibrarianRole(r) {
			t.Fatalf("%s should be valid", r)
		}
	}
	if ValidLibrarianRole("wizard") {
		t.Fatal("wizard should be invalid")
	}
}

func TestLibrarianInstances(t *testing.T) {
	s, ctx := phase5Seed(t)
	id, err := s.RegisterLibrarianInstance(ctx, &LibrarianInstance{
		Role: "detective", AgentRuntime: "stub",
	})
	if err != nil || !strings.HasPrefix(id, "detective-") {
		t.Fatalf("register: id=%s err=%v", id, err)
	}
	// Bad role
	if _, err := s.RegisterLibrarianInstance(ctx, &LibrarianInstance{Role: "wizard"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad role: %v", err)
	}

	if err := s.RecordHeartbeat(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordHeartbeat(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("heartbeat-missing: %v", err)
	}

	if err := s.SetLibrarianStatus(ctx, id, "ACTIVE"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLibrarianStatus(ctx, "missing", "ACTIVE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("status-missing: %v", err)
	}

	list, err := s.ListLibrarianInstances(ctx, "detective", "")
	if err != nil || len(list) != 1 {
		t.Fatalf("list-by-role: %+v err=%v", list, err)
	}
	if list[0].HeartbeatAt == nil {
		t.Fatal("expected heartbeat populated")
	}

	list, err = s.ListLibrarianInstances(ctx, "", "ACTIVE")
	if err != nil || len(list) != 1 {
		t.Fatalf("list-by-status: %+v err=%v", list, err)
	}
}

func TestChatThreadsAndMessages(t *testing.T) {
	s, ctx := phase5Seed(t)
	tID, err := s.OpenThread(ctx, &ChatThread{Title: "t", Intent: "observation"})
	if err != nil {
		t.Fatal(err)
	}
	mID, err := s.PostChatMessage(ctx, &ChatMessage{
		ThreadID: tID, AuthorRole: "coordinator", Intent: "observation",
		Content: "hello",
	})
	if err != nil || mID == "" {
		t.Fatalf("post: id=%s err=%v", mID, err)
	}

	msgs, err := s.ListChatMessages(ctx, tID, 0)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("list-messages: %+v err=%v", msgs, err)
	}

	threads, err := s.ListThreads(ctx, "OPEN", 10)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads-open: %+v err=%v", threads, err)
	}

	if err := s.CloseThread(ctx, tID, "wrapped"); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseThread(ctx, "missing", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("close-missing: %v", err)
	}

	threads, err = s.ListThreads(ctx, "CLOSED", 10)
	if err != nil || len(threads) != 1 || threads[0].ClosedAt == nil {
		t.Fatalf("threads-closed: %+v err=%v", threads, err)
	}
}

func TestChatMessageValidation(t *testing.T) {
	s, ctx := phase5Seed(t)
	if _, err := s.PostChatMessage(ctx, &ChatMessage{AuthorRole: "wizard", Content: "x"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad role: %v", err)
	}
	if _, err := s.PostChatMessage(ctx, &ChatMessage{AuthorRole: "judge"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty content: %v", err)
	}
}

func TestLibrarianTasks(t *testing.T) {
	s, ctx := phase5Seed(t)
	// Pre-register instances since librarian_tasks.assigned_to has an
	// FK to librarian_instances.
	if _, err := s.RegisterLibrarianInstance(ctx, &LibrarianInstance{
		InstanceID: "curator-01", Role: "curator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterLibrarianInstance(ctx, &LibrarianInstance{
		InstanceID: "curator-02", Role: "curator",
	}); err != nil {
		t.Fatal(err)
	}

	id, err := s.EnqueueTask(ctx, &LibrarianTask{
		Role: "curator", Title: "review the queue",
	})
	if err != nil || id == "" {
		t.Fatalf("enqueue: %s err=%v", id, err)
	}
	// Bad inputs
	if _, err := s.EnqueueTask(ctx, &LibrarianTask{Role: "wizard", Title: "x"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad role: %v", err)
	}
	if _, err := s.EnqueueTask(ctx, &LibrarianTask{Role: "curator"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing title: %v", err)
	}

	// Claim
	if err := s.ClaimTask(ctx, id, "curator-01"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimTask(ctx, id, "curator-02"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("re-claim: %v", err)
	}
	// Complete
	if err := s.CompleteTask(ctx, id, "ok", true); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTask(ctx, id, "x", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("re-complete: %v", err)
	}

	// failure path
	id2, _ := s.EnqueueTask(ctx, &LibrarianTask{Role: "curator", Title: "fails"})
	if err := s.CompleteTask(ctx, id2, "err", false); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListTasks(ctx, "curator", "", 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v err=%v", list, err)
	}
	list, err = s.ListTasks(ctx, "", "DONE", 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list-done: %+v err=%v", list, err)
	}
}

func TestQuartetLifecycle(t *testing.T) {
	s, ctx := phase5Seed(t)
	id, err := s.CreateQuartet(ctx, &QuartetAssignment{
		Topic:        "supersede T-X",
		Participant1: "curator-01", Participant2: "detective-01", Participant3: "conservator-01",
		Judge: "judge-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Missing fields
	if _, err := s.CreateQuartet(ctx, &QuartetAssignment{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := s.CreateQuartet(ctx, &QuartetAssignment{Topic: "t"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing participants: %v", err)
	}

	if err := s.DecideQuartet(ctx, id, "approve"); err != nil {
		t.Fatal(err)
	}
	if err := s.DecideQuartet(ctx, id, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("re-decide: %v", err)
	}

	list, err := s.ListQuartets(ctx, "DECIDED", 10)
	if err != nil || len(list) != 1 || list[0].DecidedAt == nil {
		t.Fatalf("list-decided: %+v err=%v", list, err)
	}
}

func TestExternalFindings(t *testing.T) {
	s, ctx := phase5Seed(t)
	_ = s.CreateProject(ctx, &Project{ID: "p", Name: "P"})
	eid, _ := s.CreateEntry(ctx, &Entry{
		ProjectID: "p", Type: "trap", Title: "x", Body: "y",
	})

	fid, err := s.RecordFinding(ctx, &ExternalFinding{
		AgentLens: "scout", SourceURL: "https://arxiv.org/x",
		SourceTitle: "paper", Excerpt: "snippet", Relevance: 0.85,
	})
	if err != nil || fid == "" {
		t.Fatalf("record: %s err=%v", fid, err)
	}
	if _, err := s.RecordFinding(ctx, &ExternalFinding{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing lens: %v", err)
	}

	if err := s.CorrelateFinding(ctx, fid, eid, 0); err != nil {
		t.Fatal(err)
	}
	// idempotent
	if err := s.CorrelateFinding(ctx, fid, eid, 0.7); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListFindings(ctx, "scout", 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v err=%v", list, err)
	}
	all, err := s.ListFindings(ctx, "", 10)
	if err != nil || len(all) != 1 {
		t.Fatalf("list-all: %+v err=%v", all, err)
	}
}
