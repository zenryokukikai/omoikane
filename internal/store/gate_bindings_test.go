package store

import (
	"context"
	"errors"
	"testing"
)

func TestTalkGateBindingRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.GetTalkGateBinding(ctx, "thread-0a1b2c3d"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing: err = %v, want ErrNotFound", err)
	}

	if err := s.PutTalkGateBinding(ctx, "thread-0a1b2c3d", "b-1", "i-1"); err != nil {
		t.Fatalf("put: %v", err)
	}
	b, err := s.GetTalkGateBinding(ctx, "thread-0a1b2c3d")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if b.ThreadID != "thread-0a1b2c3d" || b.BindingID != "b-1" || b.InstanceID != "i-1" {
		t.Errorf("binding: %+v", b)
	}
	if b.CreatedAt.IsZero() {
		t.Error("created_at is zero")
	}

	// Replace: address reuse gets a fresh binding generation (the old
	// one is never reopened), so Put on the same thread overwrites.
	if err := s.PutTalkGateBinding(ctx, "thread-0a1b2c3d", "b-2", "i-1"); err != nil {
		t.Fatalf("put replace: %v", err)
	}
	b, err = s.GetTalkGateBinding(ctx, "thread-0a1b2c3d")
	if err != nil {
		t.Fatalf("get after replace: %v", err)
	}
	if b.BindingID != "b-2" {
		t.Errorf("binding_id = %q, want b-2", b.BindingID)
	}

	if err := s.DeleteTalkGateBinding(ctx, "thread-0a1b2c3d"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetTalkGateBinding(ctx, "thread-0a1b2c3d"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: err = %v, want ErrNotFound", err)
	}
	// Idempotent delete.
	if err := s.DeleteTalkGateBinding(ctx, "thread-0a1b2c3d"); err != nil {
		t.Fatalf("second delete: %v", err)
	}

	// Input validation.
	if err := s.PutTalkGateBinding(ctx, "", "b", "i"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty thread: err = %v, want ErrInvalidInput", err)
	}
	if err := s.PutTalkGateBinding(ctx, "t", "", "i"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty binding: err = %v, want ErrInvalidInput", err)
	}
}

func TestSetUserLibrarianGateInstance(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// No librarian row yet → ErrNotFound (registration happens after
	// provisioning, so the row must exist).
	if err := s.SetUserLibrarianGateInstance(ctx, "alice", "0192-x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("set without row: err = %v, want ErrNotFound", err)
	}

	if err := s.CreateUser(ctx, &User{ID: "alice", Name: "Alice", Role: "member", Email: "a@x.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertUserLibrarian(ctx, &UserLibrarian{
		UserID: "alice", AgentID: "plib-alice", Name: "Lib",
	}); err != nil {
		t.Fatal(err)
	}

	ul, err := s.GetUserLibrarian(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if ul.GateInstanceID != "" {
		t.Errorf("fresh row gate_instance_id = %q, want empty", ul.GateInstanceID)
	}

	if err := s.SetUserLibrarianGateInstance(ctx, "alice", "0192aaaa-bbbb-7ccc-8ddd-eeeeffff0000"); err != nil {
		t.Fatalf("set: %v", err)
	}
	ul, err = s.GetUserLibrarian(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if ul.GateInstanceID != "0192aaaa-bbbb-7ccc-8ddd-eeeeffff0000" {
		t.Errorf("gate_instance_id = %q", ul.GateInstanceID)
	}

	// Re-provisioning (Upsert) must not clobber the registered id.
	if err := s.UpsertUserLibrarian(ctx, &UserLibrarian{
		UserID: "alice", AgentID: "plib-alice", Name: "Lib2",
	}); err != nil {
		t.Fatal(err)
	}
	ul, err = s.GetUserLibrarian(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if ul.GateInstanceID != "0192aaaa-bbbb-7ccc-8ddd-eeeeffff0000" {
		t.Errorf("gate_instance_id after upsert = %q, want preserved", ul.GateInstanceID)
	}

	if err := s.SetUserLibrarianGateInstance(ctx, "alice", ""); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty instance id: err = %v, want ErrInvalidInput", err)
	}
}

// TestTalkGateBindingCursor pins the reconnect catch-up cursor (issue
// #104 G3a): round-trip, missing-row ErrNotFound, input validation, and
// the "rebinding keeps the cursor" semantics (the cursor tracks the
// THREAD's delivery progress, not the binding generation — a fresh
// binding resumes where delivery left off, at-least-once either way).
func TestTalkGateBindingCursor(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Cursor on a thread without a binding row → ErrNotFound.
	if err := s.SetTalkGateBindingCursor(ctx, "thread-ffffffff", "msg-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cursor without binding: err = %v, want ErrNotFound", err)
	}

	if err := s.PutTalkGateBinding(ctx, "thread-0a1b2c3d", "b-1", "i-1"); err != nil {
		t.Fatalf("put: %v", err)
	}
	b, err := s.GetTalkGateBinding(ctx, "thread-0a1b2c3d")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if b.LastSentMessageID != "" {
		t.Errorf("fresh binding cursor = %q, want empty", b.LastSentMessageID)
	}

	if err := s.SetTalkGateBindingCursor(ctx, "thread-0a1b2c3d", "msg-1"); err != nil {
		t.Fatalf("set cursor: %v", err)
	}
	if b, err = s.GetTalkGateBinding(ctx, "thread-0a1b2c3d"); err != nil || b.LastSentMessageID != "msg-1" {
		t.Fatalf("cursor = %q (err %v), want msg-1", b.LastSentMessageID, err)
	}
	// Advancing overwrites.
	if err := s.SetTalkGateBindingCursor(ctx, "thread-0a1b2c3d", "msg-2"); err != nil {
		t.Fatalf("advance cursor: %v", err)
	}
	if b, err = s.GetTalkGateBinding(ctx, "thread-0a1b2c3d"); err != nil || b.LastSentMessageID != "msg-2" {
		t.Fatalf("cursor = %q (err %v), want msg-2", b.LastSentMessageID, err)
	}

	// Rebinding (address reuse → fresh binding id) keeps the cursor.
	if err := s.PutTalkGateBinding(ctx, "thread-0a1b2c3d", "b-2", "i-1"); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if b, err = s.GetTalkGateBinding(ctx, "thread-0a1b2c3d"); err != nil ||
		b.BindingID != "b-2" || b.LastSentMessageID != "msg-2" {
		t.Fatalf("after rebind: %+v (err %v), want binding b-2 with cursor msg-2", b, err)
	}

	// Input validation.
	if err := s.SetTalkGateBindingCursor(ctx, "", "m"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty thread: err = %v, want ErrInvalidInput", err)
	}
	if err := s.SetTalkGateBindingCursor(ctx, "t", ""); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty message: err = %v, want ErrInvalidInput", err)
	}
}
