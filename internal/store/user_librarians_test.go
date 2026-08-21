package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Migration 034: the table exists and the CRUD roundtrip works.
func TestUserLibrarianRoundtrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, &User{ID: "u1", Name: "U1", Role: "human"}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.GetUserLibrarian(ctx, "u1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound before setup, got %v", err)
	}

	ul := &UserLibrarian{UserID: "u1", AgentID: "plib-u1", Name: "しおり", Persona: "丁寧"}
	if err := s.UpsertUserLibrarian(ctx, ul); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetUserLibrarian(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentID != "plib-u1" || got.Name != "しおり" || got.Persona != "丁寧" ||
		got.Status != "active" || got.CreatedAt.IsZero() {
		t.Fatalf("roundtrip: %+v", got)
	}

	// Upsert = update in place, created_at preserved.
	ul2 := &UserLibrarian{UserID: "u1", AgentID: "plib-u1", Name: "新名", Persona: ""}
	if err := s.UpsertUserLibrarian(ctx, ul2); err != nil {
		t.Fatal(err)
	}
	got2, err := s.GetUserLibrarian(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if got2.Name != "新名" || got2.Persona != "" {
		t.Fatalf("update: %+v", got2)
	}
	if !got2.CreatedAt.Equal(got.CreatedAt) {
		t.Fatalf("created_at must survive re-save: %v != %v", got2.CreatedAt, got.CreatedAt)
	}
}

func TestUserLibrarianValidation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, ul := range []*UserLibrarian{
		{UserID: "", AgentID: "a", Name: "n"},
		{UserID: "u", AgentID: "", Name: "n"},
		{UserID: "u", AgentID: "a", Name: ""},
	} {
		if err := s.UpsertUserLibrarian(ctx, ul); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("want ErrInvalidInput for %+v, got %v", ul, err)
		}
	}
}

// HasAPIToken is the personal-librarian idempotency check: live api
// tokens count; expired tokens and browser sessions don't.
func TestHasAPIToken(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, &User{ID: "u1", Name: "U1", Role: "human"}); err != nil {
		t.Fatal(err)
	}

	if has, err := s.HasAPIToken(ctx, "u1", "personal-librarian"); err != nil || has {
		t.Fatalf("fresh user: has=%v err=%v", has, err)
	}

	// A session token with the same name must NOT count.
	if _, err := s.CreateSessionToken(ctx, "u1", "personal-librarian",
		[]string{"read"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if has, _ := s.HasAPIToken(ctx, "u1", "personal-librarian"); has {
		t.Fatal("session token must not satisfy HasAPIToken")
	}

	// An expired api token must NOT count.
	past := time.Now().Add(-time.Hour)
	if _, err := s.CreateToken(ctx, "u1", "personal-librarian",
		[]string{"read", "write"}, &past); err != nil {
		t.Fatal(err)
	}
	if has, _ := s.HasAPIToken(ctx, "u1", "personal-librarian"); has {
		t.Fatal("expired token must not satisfy HasAPIToken")
	}

	// A live api token counts — and only for its own name/user.
	plain, err := s.CreateToken(ctx, "u1", "personal-librarian",
		[]string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if has, _ := s.HasAPIToken(ctx, "u1", "personal-librarian"); !has {
		t.Fatal("live token should satisfy HasAPIToken")
	}
	if has, _ := s.HasAPIToken(ctx, "u1", "other-name"); has {
		t.Fatal("name must match")
	}
	if has, _ := s.HasAPIToken(ctx, "u2", "personal-librarian"); has {
		t.Fatal("user must match")
	}

	// Revoking flips it back — the failed-provision cleanup path.
	if err := s.RevokeToken(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if has, _ := s.HasAPIToken(ctx, "u1", "personal-librarian"); has {
		t.Fatal("revoked token must not satisfy HasAPIToken")
	}
}
