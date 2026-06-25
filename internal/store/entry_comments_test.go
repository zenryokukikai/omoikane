package store

import (
	"context"
	"testing"
)

func TestEntryCommentLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// A human and an agent author — both must be able to comment.
	if err := s.CreateUser(ctx, &User{ID: "alice", Name: "Alice", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, &User{ID: "bot1", Name: "mac-pi-detective", Role: "agent"}); err != nil {
		t.Fatal(err)
	}
	// Real agents get librarian_role via the invite-claim flow; set it
	// directly here so the JOIN that surfaces it can be exercised.
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET librarian_role='detective' WHERE id='bot1'`); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProject(ctx, &Project{ID: "p", Name: "Proj"}); err != nil {
		t.Fatal(err)
	}
	eid, err := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "design", Title: "T", Body: "B"})
	if err != nil {
		t.Fatal(err)
	}

	// Human posts a top-level comment.
	c1, err := s.CreateComment(ctx, eid, "alice", "  looks good but check the auth path  ", "")
	if err != nil {
		t.Fatal(err)
	}
	if c1.Body != "looks good but check the auth path" {
		t.Fatalf("body not trimmed: %q", c1.Body)
	}
	if c1.AuthorKind != "human" || c1.AuthorName != "Alice" {
		t.Fatalf("human author wrong: kind=%s name=%s", c1.AuthorKind, c1.AuthorName)
	}

	// Agent replies — author kind + librarian_role surfaced via JOIN.
	c2, err := s.CreateComment(ctx, eid, "bot1", "verified, auth path is fine", c1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c2.AuthorKind != "agent" || c2.AuthorLibrarianRole != "detective" {
		t.Fatalf("agent author wrong: kind=%s role=%s", c2.AuthorKind, c2.AuthorLibrarianRole)
	}
	if c2.ReplyTo != c1.ID {
		t.Fatalf("reply_to not set: %q", c2.ReplyTo)
	}

	// Blank body rejected.
	if _, err := s.CreateComment(ctx, eid, "alice", "   ", ""); err == nil {
		t.Fatal("blank body should be rejected")
	}

	// Reply to a comment on a DIFFERENT entry is rejected.
	eid2, _ := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "trap", Title: "T2", Body: "B2"})
	if _, err := s.CreateComment(ctx, eid2, "alice", "x", c1.ID); err == nil {
		t.Fatal("cross-entry reply should be rejected")
	}

	// List returns both, oldest first.
	list, err := s.ListComments(ctx, eid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != c1.ID || list[1].ID != c2.ID {
		t.Fatalf("list wrong: %+v", list)
	}

	// Resolve toggle.
	tru := true
	if _, err := s.UpdateComment(ctx, c1.ID, nil, &tru); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetComment(ctx, c1.ID)
	if !got.Resolved {
		t.Fatal("resolve did not stick")
	}

	// Edit body.
	nb := "edited"
	if _, err := s.UpdateComment(ctx, c1.ID, &nb, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetComment(ctx, c1.ID)
	if got.Body != "edited" {
		t.Fatalf("edit failed: %q", got.Body)
	}

	// Delete the parent cascades to its reply (ON DELETE CASCADE).
	if err := s.DeleteComment(ctx, c1.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListComments(ctx, eid)
	if len(list) != 0 {
		t.Fatalf("cascade delete failed, remaining: %+v", list)
	}

	// Deleting a missing comment is ErrNotFound.
	if err := s.DeleteComment(ctx, "C-deadbeef"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
