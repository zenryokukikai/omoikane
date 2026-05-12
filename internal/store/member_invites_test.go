//go:build sqlite_fts5

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCreateAndGetMemberInvitation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &User{ID: "admin", Name: "admin", Role: "admin"})

	inv, err := s.CreateMemberInvitation(ctx, "admin", "new@x.com", "member", "qa")
	if err != nil {
		t.Fatal(err)
	}
	if inv.TargetEmail != "new@x.com" || inv.TargetRole != "member" || inv.Note != "qa" {
		t.Fatalf("bad invite: %+v", inv)
	}
	if inv.ExpiresAt.Before(time.Now()) {
		t.Fatal("expires in the past")
	}

	// Get back by code.
	got, err := s.GetMemberInvitation(ctx, inv.Code)
	if err != nil {
		t.Fatal(err)
	}
	if got.Code != inv.Code {
		t.Errorf("code roundtrip: %q vs %q", got.Code, inv.Code)
	}
}

// Email lowercasing: invites with different casings normalize to the
// same email — invitee can sign in with Google's casing, lookup still
// finds the row.
func TestMemberInvitationEmailIsLowercased(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &User{ID: "admin", Name: "admin", Role: "admin"})
	inv, _ := s.CreateMemberInvitation(ctx, "admin", "MixedCase@Example.COM", "member", "")
	if inv.TargetEmail != "mixedcase@example.com" {
		t.Errorf("not lowercased: %s", inv.TargetEmail)
	}
	// Lookup with different casing finds it.
	got, err := s.FindOpenMemberInvitationForEmail(ctx, "MIXEDCASE@example.com")
	if err != nil {
		t.Fatalf("lookup failed: %v", err)
	}
	if got.Code != inv.Code {
		t.Errorf("lookup returned wrong invite")
	}
}

// FindOpen excludes used invitations.
func TestFindOpenIgnoresUsedInvitations(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &User{ID: "admin", Name: "admin", Role: "admin"})
	inv, _ := s.CreateMemberInvitation(ctx, "admin", "claimee@x.com", "member", "")
	// Pretend a user claimed it.
	_ = s.CreateUser(ctx, &User{ID: "claimee", Name: "claimee", Role: "member"})
	if err := s.MarkMemberInvitationUsed(ctx, inv.Code, "claimee"); err != nil {
		t.Fatalf("mark used: %v", err)
	}
	// Now FindOpen should not return it.
	_, err := s.FindOpenMemberInvitationForEmail(ctx, "claimee@x.com")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// MarkUsed twice → second call is ErrAlreadyExists (race-condition guard).
func TestMarkMemberInvitationUsedIsAtomic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &User{ID: "admin", Name: "admin", Role: "admin"})
	_ = s.CreateUser(ctx, &User{ID: "claimee", Name: "claimee", Role: "member"})
	inv, _ := s.CreateMemberInvitation(ctx, "admin", "claimee@x.com", "member", "")

	if err := s.MarkMemberInvitationUsed(ctx, inv.Code, "claimee"); err != nil {
		t.Fatal(err)
	}
	err := s.MarkMemberInvitationUsed(ctx, inv.Code, "claimee")
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("want ErrAlreadyExists on second mark, got %v", err)
	}
}

// ProvisionGoogleUserWithRole honors role on CREATE only — existing
// users keep their role.
func TestProvisionGoogleUserWithRoleHonorsRoleOnCreate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Fresh email → create as admin per the invite's target_role.
	u, err := s.ProvisionGoogleUserWithRole(ctx, "fresh@x.com", "sub-fresh", "Fresh", "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != "admin" {
		t.Errorf("create-with-role failed: %s", u.Role)
	}

	// Existing email + role override → role NOT changed (we don't
	// silently re-role on every login).
	u2, err := s.ProvisionGoogleUserWithRole(ctx, "fresh@x.com", "sub-fresh", "Fresh", "", "member")
	if err != nil {
		t.Fatal(err)
	}
	if u2.Role != "admin" {
		t.Errorf("existing user role was silently changed: %s", u2.Role)
	}
}

// UpdateUserRole store-level tests (the API tests cover the HTTP
// surface; these lock the store invariants directly).
func TestStoreUpdateUserRole(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &User{ID: "admin", Name: "admin", Role: "admin"})
	_ = s.CreateUser(ctx, &User{ID: "alice", Name: "Alice", Role: "member"})

	// Promote alice.
	u, err := s.UpdateUserRole(ctx, "alice", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != "admin" {
		t.Errorf("not promoted: %s", u.Role)
	}

	// Demote admin (the original) — should succeed now that alice is admin.
	u, err = s.UpdateUserRole(ctx, "admin", "member")
	if err != nil {
		t.Fatalf("demote with another admin present: %v", err)
	}
	if u.Role != "member" {
		t.Errorf("not demoted: %s", u.Role)
	}

	// Now alice is the only admin — demoting her should fail.
	_, err = s.UpdateUserRole(ctx, "alice", "member")
	if err == nil {
		t.Fatal("last-admin demote should be rejected")
	}
}

func TestStoreUpdateUserRoleRejectsAgent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &User{ID: "admin", Name: "admin", Role: "admin"})
	_ = s.CreateUser(ctx, &User{
		ID: "bot", Name: "bot", Role: "agent", ParentUserID: "admin",
	})
	_, err := s.UpdateUserRole(ctx, "bot", "admin")
	if err == nil {
		t.Fatal("agent role change should be rejected")
	}
}

func TestStoreUpdateUserRoleRejectsBadRole(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.CreateUser(ctx, &User{ID: "admin", Name: "admin", Role: "admin"})
	_, err := s.UpdateUserRole(ctx, "admin", "superadmin")
	if err == nil {
		t.Fatal("bad role should be rejected")
	}
}
