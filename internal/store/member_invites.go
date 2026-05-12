package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// MemberInviteTTL is how long a human-to-human invitation stays valid.
// 7 days — longer than agent invites (24h) because the redemption flow
// involves a real human reading email-or-Slack, going through OAuth,
// etc. rather than an agent firing off an HTTP call.
const MemberInviteTTL = 7 * 24 * time.Hour

// MemberInvitation is one row of member_invitations.
type MemberInvitation struct {
	Code          string
	InviterUserID string
	TargetEmail   string
	TargetRole    string // 'admin' | 'member'
	Note          string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	UsedAt        *time.Time
	UsedByUser    string
}

// CreateMemberInvitation mints an invitation. target_email is required
// (we agreed: human invites are addressed); target_role defaults to
// 'member' if blank. Inviter must already exist as a human user (we
// don't enforce here — caller responsibility — but the FK will catch
// gibberish).
func (s *Store) CreateMemberInvitation(ctx context.Context, inviterUserID, targetEmail, targetRole, note string) (*MemberInvitation, error) {
	if inviterUserID == "" {
		return nil, fmt.Errorf("%w: inviter required", ErrInvalidInput)
	}
	targetEmail = strings.ToLower(strings.TrimSpace(targetEmail))
	if targetEmail == "" {
		return nil, fmt.Errorf("%w: target_email required", ErrInvalidInput)
	}
	if targetRole == "" {
		targetRole = "member"
	}
	switch targetRole {
	case "admin", "member":
		// ok
	default:
		return nil, fmt.Errorf("%w: target_role must be admin|member", ErrInvalidInput)
	}

	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	code := hex.EncodeToString(b[:])
	exp := time.Now().Add(MemberInviteTTL).UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO member_invitations(code, inviter_user_id, target_email, target_role, note, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		code, inviterUserID, targetEmail, targetRole, nullable(note), exp)
	if err != nil {
		return nil, translateErr(err)
	}
	return s.GetMemberInvitation(ctx, code)
}

// GetMemberInvitation looks up by code. Returns ErrNotFound for unknown
// codes.
func (s *Store) GetMemberInvitation(ctx context.Context, code string) (*MemberInvitation, error) {
	var (
		inv    MemberInvitation
		usedAt nullTimeBox
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT code, inviter_user_id, target_email, target_role, COALESCE(note,''),
		       created_at, expires_at, used_at, COALESCE(used_by_user,'')
		FROM member_invitations WHERE code = ?`, code).Scan(
		&inv.Code, &inv.InviterUserID, &inv.TargetEmail, &inv.TargetRole,
		&inv.Note, &inv.CreatedAt, &inv.ExpiresAt, &usedAt, &inv.UsedByUser)
	if err != nil {
		return nil, translateErr(err)
	}
	if usedAt.Valid {
		t := usedAt.Time
		inv.UsedAt = &t
	}
	return &inv, nil
}

// ListMemberInvitations returns invitations issued by the given human
// (or all invitations if inviterUserID == "" — for admin listing).
func (s *Store) ListMemberInvitations(ctx context.Context, inviterUserID string) ([]*MemberInvitation, error) {
	var (
		rows interface{ next(*MemberInvitation, *nullTimeBox) error }
	)
	_ = rows // suppress unused — written below with a closure
	q := `SELECT code, inviter_user_id, target_email, target_role, COALESCE(note,''),
	             created_at, expires_at, used_at, COALESCE(used_by_user,'')
	      FROM member_invitations`
	args := []any{}
	if inviterUserID != "" {
		q += ` WHERE inviter_user_id = ?`
		args = append(args, inviterUserID)
	}
	q += ` ORDER BY created_at DESC LIMIT 200`
	r, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[MemberInvitation](r, func(c rowScanner, inv *MemberInvitation) error {
		var usedAt nullTimeBox
		if err := c.Scan(&inv.Code, &inv.InviterUserID, &inv.TargetEmail,
			&inv.TargetRole, &inv.Note, &inv.CreatedAt, &inv.ExpiresAt,
			&usedAt, &inv.UsedByUser); err != nil {
			return err
		}
		if usedAt.Valid {
			t := usedAt.Time
			inv.UsedAt = &t
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*MemberInvitation, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// FindOpenMemberInvitationForEmail returns the most-recent OPEN
// (unused, unexpired) invitation matching the email, or ErrNotFound.
// Email lookup is case-insensitive — we store lowercase, callers can
// pass any case.
func (s *Store) FindOpenMemberInvitationForEmail(ctx context.Context, email string) (*MemberInvitation, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, ErrNotFound
	}
	var (
		inv    MemberInvitation
		usedAt nullTimeBox
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT code, inviter_user_id, target_email, target_role, COALESCE(note,''),
		       created_at, expires_at, used_at, COALESCE(used_by_user,'')
		FROM member_invitations
		WHERE target_email = ? AND used_at IS NULL AND expires_at > CURRENT_TIMESTAMP
		ORDER BY created_at DESC LIMIT 1`, email).Scan(
		&inv.Code, &inv.InviterUserID, &inv.TargetEmail, &inv.TargetRole,
		&inv.Note, &inv.CreatedAt, &inv.ExpiresAt, &usedAt, &inv.UsedByUser)
	if err != nil {
		return nil, translateErr(err)
	}
	return &inv, nil
}

// MarkMemberInvitationUsed records that `code` was redeemed by
// `userID`. Returns ErrNotFound if the code doesn't exist,
// ErrAlreadyExists if it's already been used (race condition guard).
// Idempotent only when used_by_user matches.
func (s *Store) MarkMemberInvitationUsed(ctx context.Context, code, userID string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE member_invitations
		SET used_at = CURRENT_TIMESTAMP, used_by_user = ?
		WHERE code = ? AND used_at IS NULL`, userID, code)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Either unknown code or already used — distinguish for the
		// caller so error reporting is precise.
		var existing string
		err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(used_by_user,'') FROM member_invitations WHERE code = ?`,
			code).Scan(&existing)
		if err != nil {
			return ErrNotFound
		}
		return fmt.Errorf("%w: invitation already used", ErrAlreadyExists)
	}
	return nil
}

// UpdateUserRole changes role between 'admin' and 'member'. Refuses to
// touch agent users (their role is part of their identity, not a
// permission level — promoting an agent to admin would break the
// parent_user_id relationship). Refuses to demote the LAST admin to
// prevent lockout.
func (s *Store) UpdateUserRole(ctx context.Context, userID, newRole string) (*User, error) {
	if newRole != "admin" && newRole != "member" {
		return nil, fmt.Errorf("%w: role must be admin|member", ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var currentRole string
	err = tx.QueryRowContext(ctx, `SELECT role FROM users WHERE id = ?`, userID).Scan(&currentRole)
	if err != nil {
		return nil, translateErr(err)
	}
	if currentRole == "agent" {
		return nil, fmt.Errorf("%w: cannot change role of an agent user (use the agent flow instead)", ErrInvalidInput)
	}
	if currentRole == newRole {
		// No-op. Commit empty tx and return current state.
		_ = tx.Commit()
		return s.GetUser(ctx, userID)
	}
	// Lockout guard: if demoting an admin, ensure another admin exists.
	if currentRole == "admin" && newRole != "admin" {
		var adminCount int
		err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM users WHERE role = 'admin' AND id != ?`, userID).Scan(&adminCount)
		if err != nil {
			return nil, err
		}
		if adminCount == 0 {
			return nil, fmt.Errorf("%w: cannot demote the last admin (would lock everyone out)", ErrInvalidInput)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, newRole, userID); err != nil {
		return nil, translateErr(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetUser(ctx, userID)
}
