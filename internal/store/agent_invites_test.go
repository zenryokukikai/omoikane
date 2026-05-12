package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCreateAndGetAgentInvitation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &User{ID: "alice", Name: "Alice"})

	inv, err := s.CreateAgentInvitation(ctx, "alice", "for lipsync project")
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Code) != 16 {
		t.Fatalf("code: %s", inv.Code)
	}
	if inv.InviterUserID != "alice" {
		t.Fatalf("inviter: %s", inv.InviterUserID)
	}
	if inv.Note != "for lipsync project" {
		t.Fatalf("note: %s", inv.Note)
	}
	if inv.UsedAt != nil {
		t.Fatal("should be unused")
	}

	got, _ := s.GetAgentInvitation(ctx, inv.Code)
	if got.Code != inv.Code {
		t.Fatalf("get: %+v", got)
	}
}

func TestCreateAgentInvitationValidation(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateAgentInvitation(context.Background(), "", "x"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty inviter: %v", err)
	}
}

func TestRedeemAgentInvitationHappyPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &User{ID: "alice", Name: "Alice"})
	inv, _ := s.CreateAgentInvitation(ctx, "alice", "")

	reg, err := s.RedeemAgentInvitation(ctx, inv.Code, "claude-x", "test agent")
	if err != nil {
		t.Fatal(err)
	}
	if reg.AgentUser.ParentUserID != "alice" {
		t.Fatalf("parent: %s", reg.AgentUser.ParentUserID)
	}
	if reg.AgentUser.Role != "agent" {
		t.Fatalf("role: %s", reg.AgentUser.Role)
	}
	if reg.AgentUser.Description != "test agent" {
		t.Fatalf("description: %s", reg.AgentUser.Description)
	}
	if reg.APIToken == "" {
		t.Fatal("no api token")
	}

	// Invite is now marked used
	got, _ := s.GetAgentInvitation(ctx, inv.Code)
	if got.UsedAt == nil {
		t.Fatal("used_at not set")
	}
	if got.UsedByAgent != reg.AgentUser.ID {
		t.Fatalf("used_by: %s", got.UsedByAgent)
	}

	// Token actually works
	tok, _ := s.LookupToken(ctx, reg.APIToken)
	if tok.UserID != reg.AgentUser.ID {
		t.Fatalf("token user: %s", tok.UserID)
	}
}

func TestRedeemAgentInvitationReuse(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &User{ID: "alice", Name: "Alice"})
	inv, _ := s.CreateAgentInvitation(ctx, "alice", "")

	if _, err := s.RedeemAgentInvitation(ctx, inv.Code, "a1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemAgentInvitation(ctx, inv.Code, "a2", ""); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("expected already-used: %v", err)
	}
}

func TestRedeemAgentInvitationExpired(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &User{ID: "alice", Name: "Alice"})
	inv, _ := s.CreateAgentInvitation(ctx, "alice", "")
	// Backdate expiry
	if _, err := s.DB().Exec(
		`UPDATE agent_invitations SET expires_at = datetime('now','-1 hour') WHERE code = ?`,
		inv.Code); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemAgentInvitation(ctx, inv.Code, "a", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired: %v", err)
	}
}

func TestRedeemAgentInvitationMissingCode(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.RedeemAgentInvitation(context.Background(), "missing", "a", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestRedeemAgentInvitationEmptyName(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &User{ID: "alice", Name: "Alice"})
	inv, _ := s.CreateAgentInvitation(ctx, "alice", "")
	if _, err := s.RedeemAgentInvitation(ctx, inv.Code, "", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty name: %v", err)
	}
}

func TestListAgentInvitations(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &User{ID: "alice", Name: "Alice"})
	_ = s.CreateUser(ctx, &User{ID: "bob", Name: "Bob"})

	_, _ = s.CreateAgentInvitation(ctx, "alice", "first")
	_, _ = s.CreateAgentInvitation(ctx, "alice", "second")
	_, _ = s.CreateAgentInvitation(ctx, "bob", "bob's")

	a, _ := s.ListAgentInvitations(ctx, "alice")
	if len(a) != 2 {
		t.Fatalf("alice: %d", len(a))
	}
	b, _ := s.ListAgentInvitations(ctx, "bob")
	if len(b) != 1 {
		t.Fatalf("bob: %d", len(b))
	}
}

// Sanity: TTL is set correctly so the test isn't accidentally racy.
func TestInviteCodeTTL(t *testing.T) {
	if InviteCodeTTL < time.Hour {
		t.Fatalf("TTL too short: %s", InviteCodeTTL)
	}
}
