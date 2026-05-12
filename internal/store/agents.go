package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// ClaimCodeTTL is how long an unclaimed agent registration lingers
// before its claim code is no longer valid. Agents whose codes expire
// are NOT auto-deleted — the human can still see the agent in the
// admin UI and claim it manually. (Future Phase B work: garbage-collect
// expired unclaimed agents after a longer TTL.)
const ClaimCodeTTL = 72 * time.Hour

// AgentRegistration is the result of POST /v1/agents/register. It
// bundles the freshly-minted agent user, the one-time API token, and
// the claim URL the agent should pass to its human.
type AgentRegistration struct {
	AgentUser  *User
	APIToken   string // plain token, shown to caller exactly once
	ClaimCode  string // 8-char URL-safe one-time code
	ClaimURL   string // populated by the API layer
	ExpiresAt  time.Time
}

// RegisterAgent creates an unclaimed agent user + an API token for it,
// returning the plain token and a claim code. Both are time-limited
// (claim code by ClaimCodeTTL; API token never expires once claimed,
// but the caller can decide to wait for a successful claim before
// using it heavily).
//
// `name` is the human-readable identifier the agent provides — usually
// the agent runtime + its purpose, e.g. "claude-code-lipsync-dev".
// Lowercased; whitespace trimmed.
func (s *Store) RegisterAgent(ctx context.Context, name, description string) (*AgentRegistration, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: name required", ErrInvalidInput)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	uid, err := newUserID()
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO users(id, name, role, description)
		VALUES (?, ?, 'agent', ?)`,
		uid, name, nullable(description)); err != nil {
		return nil, translateErr(err)
	}

	// Mint a token immediately — the agent needs it now. read+write,
	// no admin. parent_user_id is set later by Claim.
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

	// Generate claim code. 16 hex chars is short enough to display, long
	// enough to resist guessing (64 bits).
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	code := hex.EncodeToString(b[:])
	expiresAt := time.Now().Add(ClaimCodeTTL).UTC()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_claim_codes(code, agent_user_id, expires_at)
		VALUES (?, ?, ?)`, code, uid, expiresAt); err != nil {
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
		ClaimCode: code,
		ExpiresAt: expiresAt,
	}, nil
}

// AgentClaim is the unclaimed-agent view exposed at GET
// /v1/agents/claim/{code} so the human can see what they're about to
// adopt before pressing the button.
type AgentClaim struct {
	Code      string
	AgentUser *User
	ExpiresAt time.Time
	ClaimedAt *time.Time
	ClaimedBy string
}

// GetClaimByCode returns the claim record for the given code. Returns
// ErrNotFound when the code doesn't exist or has expired and never been
// claimed.
func (s *Store) GetClaimByCode(ctx context.Context, code string) (*AgentClaim, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT code, agent_user_id, expires_at, claimed_at, COALESCE(claimed_by,'')
		FROM agent_claim_codes WHERE code = ?`, code)
	var (
		c         AgentClaim
		agentID   string
		claimedAt nullTimeBox
	)
	if err := row.Scan(&c.Code, &agentID, &c.ExpiresAt, &claimedAt, &c.ClaimedBy); err != nil {
		return nil, translateErr(err)
	}
	if claimedAt.Valid {
		t := claimedAt.Time
		c.ClaimedAt = &t
	}
	// Reject expired unclaimed codes — but show already-claimed ones so
	// the UI can say "already claimed".
	if c.ClaimedAt == nil && c.ExpiresAt.Before(time.Now()) {
		return nil, ErrNotFound
	}
	u, err := s.GetUser(ctx, agentID)
	if err != nil {
		return nil, err
	}
	c.AgentUser = u
	return &c, nil
}

// ClaimAgent transfers ownership of the agent identified by `code` to
// the supplied human user. Sets agent_user.parent_user_id, stamps the
// claim record, and is idempotent for the same (code, human) pair.
//
// Returns ErrNotFound when the code is unknown or expired. Returns
// ErrAlreadyExists when the code was claimed by a different human.
func (s *Store) ClaimAgent(ctx context.Context, code, humanUserID string) error {
	if humanUserID == "" {
		return fmt.Errorf("%w: human user_id required", ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		agentID, claimedBy string
		expiresAt          time.Time
		claimedAt          nullTimeBox
	)
	err = tx.QueryRowContext(ctx, `
		SELECT agent_user_id, expires_at, claimed_at, COALESCE(claimed_by,'')
		FROM agent_claim_codes WHERE code = ?`, code).Scan(
		&agentID, &expiresAt, &claimedAt, &claimedBy)
	if err != nil {
		return translateErr(err)
	}
	if claimedAt.Valid && claimedBy != humanUserID {
		return fmt.Errorf("%w: already claimed by a different user", ErrAlreadyExists)
	}
	if !claimedAt.Valid && expiresAt.Before(time.Now()) {
		return ErrNotFound
	}

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_claim_codes SET claimed_at = ?, claimed_by = ?
		WHERE code = ?`, now, humanUserID, code); err != nil {
		return translateErr(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET parent_user_id = ? WHERE id = ?`,
		humanUserID, agentID); err != nil {
		return translateErr(err)
	}
	return tx.Commit()
}

// ListAgentsForHuman returns the agents currently parented under
// `humanUserID`. Useful for the dashboard "My agents" page.
func (s *Store) ListAgentsForHuman(ctx context.Context, humanUserID string) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userSelect+` FROM users WHERE parent_user_id = ? ORDER BY created_at`,
		humanUserID)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[User](rows, func(c rowScanner, u *User) error {
		return scanUser(c, u)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*User, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// ListUsers returns users for the directory/profile-browse endpoints.
// Filters:
//   - roleFilter: "" for any role, or one of "admin"|"member"|"agent".
//   - limit: 0 → no cap (the user table is small; callers should still
//     pass a sane cap to keep responses bounded).
//
// Order is creation time ascending so newcomers appear at the bottom of
// listings, which is the convention humans expect for "who's around"
// directories. Use a separate query path if you need leaderboard-style
// ordering by activity.
func (s *Store) ListUsers(ctx context.Context, roleFilter string, limit int) ([]*User, error) {
	q := `SELECT ` + userSelect + ` FROM users`
	args := []any{}
	if roleFilter != "" {
		q += ` WHERE role = ?`
		args = append(args, roleFilter)
	}
	q += ` ORDER BY created_at`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[User](rows, func(c rowScanner, u *User) error {
		return scanUser(c, u)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*User, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}
