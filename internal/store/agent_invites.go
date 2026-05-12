package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// InviteCodeTTL is how long a human-issued invitation stays valid.
const InviteCodeTTL = 24 * time.Hour

// AgentInvitation is one row of agent_invitations.
type AgentInvitation struct {
	Code           string
	InviterUserID  string
	Note           string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	UsedAt         *time.Time
	UsedByAgent    string
}

// CreateAgentInvitation mints a fresh invitation under the supplied
// human user. The returned code is what they hand to the prospective
// agent; the inviter's user_id will become the agent's parent_user_id
// at redemption time.
func (s *Store) CreateAgentInvitation(ctx context.Context, inviterUserID, note string) (*AgentInvitation, error) {
	if inviterUserID == "" {
		return nil, fmt.Errorf("%w: inviter required", ErrInvalidInput)
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	code := hex.EncodeToString(b[:])
	exp := time.Now().Add(InviteCodeTTL).UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_invitations(code, inviter_user_id, note, expires_at)
		VALUES (?, ?, ?, ?)`,
		code, inviterUserID, nullable(note), exp)
	if err != nil {
		return nil, translateErr(err)
	}
	return s.GetAgentInvitation(ctx, code)
}

// GetAgentInvitation looks up a code. Returns ErrNotFound for unknown
// codes; the caller can check `UsedAt != nil` and `ExpiresAt < now` to
// decide whether the code is still usable.
func (s *Store) GetAgentInvitation(ctx context.Context, code string) (*AgentInvitation, error) {
	var (
		inv       AgentInvitation
		usedAt    nullTimeBox
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT code, inviter_user_id, COALESCE(note,''), created_at, expires_at,
		       used_at, COALESCE(used_by_agent,'')
		FROM agent_invitations WHERE code = ?`, code).Scan(
		&inv.Code, &inv.InviterUserID, &inv.Note,
		&inv.CreatedAt, &inv.ExpiresAt, &usedAt, &inv.UsedByAgent)
	if err != nil {
		return nil, translateErr(err)
	}
	if usedAt.Valid {
		t := usedAt.Time
		inv.UsedAt = &t
	}
	return &inv, nil
}

// ListAgentInvitations returns invitations issued by the given human,
// most-recent first.
func (s *Store) ListAgentInvitations(ctx context.Context, inviterUserID string) ([]*AgentInvitation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT code, inviter_user_id, COALESCE(note,''), created_at, expires_at,
		       used_at, COALESCE(used_by_agent,'')
		FROM agent_invitations WHERE inviter_user_id = ? ORDER BY created_at DESC LIMIT 200`,
		inviterUserID)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[AgentInvitation](rows, func(c rowScanner, inv *AgentInvitation) error {
		var usedAt nullTimeBox
		if err := c.Scan(&inv.Code, &inv.InviterUserID, &inv.Note,
			&inv.CreatedAt, &inv.ExpiresAt, &usedAt, &inv.UsedByAgent); err != nil {
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
	out := make([]*AgentInvitation, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// RedeemAgentInvitation atomically:
//   1. Verifies the code is still valid (exists, not used, not expired)
//   2. Creates the agent user with parent_user_id = inviter
//   3. Mints an api_token for the agent
//   4. Marks the code used
//
// Returns the same AgentRegistration shape as RegisterAgent — but no
// claim_code, because adoption is implicit (the inviter is already the
// parent). Caller passes a friendly name + description for the agent.
func (s *Store) RedeemAgentInvitation(ctx context.Context, code, name, description string) (*AgentRegistration, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: name required", ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		inviterUserID string
		expiresAt     time.Time
		usedAt        nullTimeBox
	)
	err = tx.QueryRowContext(ctx, `
		SELECT inviter_user_id, expires_at, used_at
		FROM agent_invitations WHERE code = ?`, code).Scan(
		&inviterUserID, &expiresAt, &usedAt)
	if err != nil {
		return nil, translateErr(err)
	}
	if usedAt.Valid {
		return nil, fmt.Errorf("%w: invitation already used", ErrAlreadyExists)
	}
	if expiresAt.Before(time.Now()) {
		return nil, fmt.Errorf("%w: invitation expired", ErrNotFound)
	}

	// Create agent user with parent already set
	uid, err := newUserID()
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO users(id, name, role, description, parent_user_id)
		VALUES (?, ?, 'agent', ?, ?)`,
		uid, name, nullable(description), inviterUserID); err != nil {
		return nil, translateErr(err)
	}

	// Mint API token
	plain, err := GenerateToken()
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO api_tokens(token_hash, user_id, name, scopes, token_type)
		VALUES (?, ?, ?, ?, 'api')`,
		HashToken(plain), uid, name, "read,write"); err != nil {
		return nil, translateErr(err)
	}

	// Mark code used
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_invitations SET used_at = ?, used_by_agent = ?
		WHERE code = ?`, now, uid, code); err != nil {
		return nil, translateErr(err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	u, err := s.GetUser(ctx, uid)
	if err != nil {
		return nil, err
	}
	return &AgentRegistration{
		AgentUser: u,
		APIToken:  plain,
		// No ClaimCode — adoption is implicit at redemption
	}, nil
}
