package store

import (
	"context"
	"testing"
)

// Reply-to-reply lands in the SAME thread: thread_root propagates from
// the parent, and a fresh top-level comment roots its own thread.
func TestCommentThreadRootPropagation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, &Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, &User{ID: "u1", Name: "u1", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	eid, err := s.CreateEntry(ctx, &Entry{ProjectID: "p", Type: "design", Title: "T", Body: "B"})
	if err != nil {
		t.Fatal(err)
	}

	root, err := s.CreateComment(ctx, eid, "u1", "root", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if root.ThreadRoot != root.ID {
		t.Fatalf("root thread_root = %q, want own id %q", root.ThreadRoot, root.ID)
	}
	reply, err := s.CreateComment(ctx, eid, "u1", "reply", root.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply.ThreadRoot != root.ID {
		t.Fatalf("reply thread_root = %q, want %q", reply.ThreadRoot, root.ID)
	}
	// The key case: replying to the REPLY still belongs to root's thread.
	nested, err := s.CreateComment(ctx, eid, "u1", "nested", reply.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if nested.ThreadRoot != root.ID {
		t.Fatalf("nested thread_root = %q, want %q", nested.ThreadRoot, root.ID)
	}
	if nested.ReplyTo != reply.ID {
		t.Fatalf("nested reply_to = %q, want %q", nested.ReplyTo, reply.ID)
	}
	// Another top-level comment starts its own thread.
	other, err := s.CreateComment(ctx, eid, "u1", "other", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if other.ThreadRoot != other.ID || other.ThreadRoot == root.ID {
		t.Fatalf("other thread_root = %q", other.ThreadRoot)
	}
}
