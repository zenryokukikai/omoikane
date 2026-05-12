package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// HashToken is the canonical hash used to look up Bearer tokens. We never
// store plain tokens — only this hash.
func HashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// GenerateToken returns 64 hex chars (32 bytes) of crypto/rand. The plain
// token is shown to the caller exactly once at issue time. Uses randRead
// (overridable in tests) so the otherwise-unreachable rand.Read failure
// branch is exercisable.
func GenerateToken() (string, error) {
	var buf [32]byte
	if _, err := randRead(buf[:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

func (s *Store) CreateUser(ctx context.Context, u *User) error {
	if u.ID == "" || u.Name == "" {
		return ErrInvalidInput
	}
	if u.Role == "" {
		u.Role = "member"
	}
	// description and parent_user_id are written here so seed/test paths
	// and any caller that wants to set the full identity in one shot can
	// do so. The agent registration paths (RegisterAgent /
	// RedeemAgentInvitation) take their own paths and don't go through
	// CreateUser, so this only matters for direct callers — but when it
	// does matter, silently dropping the fields would be surprising.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users(id, name, role, email, google_sub, avatar_url, parent_user_id, description)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Name, u.Role,
		nullable(strings.ToLower(strings.TrimSpace(u.Email))),
		nullable(u.GoogleSub),
		nullable(u.AvatarURL),
		nullable(u.ParentUserID),
		nullable(u.Description))
	return translateErr(err)
}

// userSelect lists every column we scan into a User (in fixed order).
// Centralised so the half-dozen lookup paths stay in sync.
const userSelect = `
	id, name, role, created_at,
	COALESCE(email,''), COALESCE(google_sub,''),
	COALESCE(avatar_url,''), last_login_at, email_verified_at,
	COALESCE(parent_user_id,''), COALESCE(description,'')`

func scanUser(r scanOne, u *User) error {
	var last, verified nullTimeBox
	if err := r.Scan(&u.ID, &u.Name, &u.Role, &u.CreatedAt,
		&u.Email, &u.GoogleSub, &u.AvatarURL, &last, &verified,
		&u.ParentUserID, &u.Description); err != nil {
		return err
	}
	if last.Valid {
		t := last.Time
		u.LastLoginAt = &t
	}
	if verified.Valid {
		t := verified.Time
		u.EmailVerifiedAt = &t
	}
	return nil
}

func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	var u User
	err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userSelect+` FROM users WHERE id = ?`, id), &u)
	if err != nil {
		return nil, translateErr(err)
	}
	return &u, nil
}

// GetUserByEmail returns the user with the matching (case-insensitive)
// email. Email is normalised to lowercase on write so a simple equality
// query suffices.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, ErrNotFound
	}
	var u User
	err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userSelect+` FROM users WHERE email = ?`, email), &u)
	if err != nil {
		return nil, translateErr(err)
	}
	return &u, nil
}

// GetUserByGoogleSub returns the user with the matching Google subject
// identifier. `sub` is the canonical, immutable identifier — email can
// change but `sub` does not.
func (s *Store) GetUserByGoogleSub(ctx context.Context, sub string) (*User, error) {
	if sub == "" {
		return nil, ErrNotFound
	}
	var u User
	err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userSelect+` FROM users WHERE google_sub = ?`, sub), &u)
	if err != nil {
		return nil, translateErr(err)
	}
	return &u, nil
}

// UserProfilePatch is the set of self-editable fields. Pointer
// semantics: nil = "leave alone", non-nil = "set to this value".
// We deliberately exclude email/google_sub/role/parent_user_id/id —
// those are controlled by other paths (OAuth handshake, admin
// promotion, agent claim flow) and self-editing them would either
// break invariants or escalate privileges.
type UserProfilePatch struct {
	Name        *string
	Description *string
	AvatarURL   *string
}

// UpdateUserProfile applies the patch and returns the post-update
// user. Only the fields whose pointer is non-nil are touched. Returns
// ErrInvalidInput if Name is set to an empty/whitespace string (name
// is required by the users table), ErrNotFound if no user has that id.
func (s *Store) UpdateUserProfile(ctx context.Context, userID string, p UserProfilePatch) (*User, error) {
	if userID == "" {
		return nil, ErrInvalidInput
	}
	// Build dynamic UPDATE — only columns the caller actually set.
	sets := []string{}
	args := []any{}
	if p.Name != nil {
		trimmed := strings.TrimSpace(*p.Name)
		if trimmed == "" {
			return nil, fmt.Errorf("%w: name cannot be empty", ErrInvalidInput)
		}
		sets = append(sets, "name = ?")
		args = append(args, trimmed)
	}
	if p.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, nullable(strings.TrimSpace(*p.Description)))
	}
	if p.AvatarURL != nil {
		sets = append(sets, "avatar_url = ?")
		args = append(args, nullable(strings.TrimSpace(*p.AvatarURL)))
	}
	if len(sets) == 0 {
		// Nothing to update — return current user. This isn't an error;
		// idempotent no-op PATCH is a legitimate use case.
		return s.GetUser(ctx, userID)
	}
	args = append(args, userID)
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return nil, translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrNotFound
	}
	return s.GetUser(ctx, userID)
}

// SetUserEmail updates the email (and ensures lowercasing). Returns
// ErrAlreadyExists when another user already owns that email.
func (s *Store) SetUserEmail(ctx context.Context, userID, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return fmt.Errorf("%w: email required", ErrInvalidInput)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET email = ? WHERE id = ?`, email, userID)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// LinkGoogleIdentity attaches a google_sub (+ optional avatar) to an
// existing user. Use when a Google login matches an existing user by
// email but the user was previously bootstrap-only.
func (s *Store) LinkGoogleIdentity(ctx context.Context, userID, googleSub, avatarURL string) error {
	if googleSub == "" {
		return fmt.Errorf("%w: google_sub required", ErrInvalidInput)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE users
		SET google_sub = ?, avatar_url = COALESCE(NULLIF(?, ''), avatar_url)
		WHERE id = ?`, googleSub, avatarURL, userID)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordLogin stamps last_login_at = now. Best-effort; ignored if the
// user vanished mid-flight.
func (s *Store) RecordLogin(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_login_at = CURRENT_TIMESTAMP WHERE id = ?`, userID)
	return translateErr(err)
}

// ProvisionGoogleUser is the canonical "first Google login from this
// identity" helper. It tries, in order:
//
//  1. Find by `google_sub` → return existing user (already linked)
//  2. Find by `email` → link Google to existing user, return it
//  3. Create a new user with role="member" (called only when the caller
//     has already verified the email/domain is allowed)
//
// `name` may be the Google display name; we keep `email`'s local-part
// as a fallback.
func (s *Store) ProvisionGoogleUser(ctx context.Context, email, googleSub, name, avatarURL string) (*User, error) {
	if existing, err := s.GetUserByGoogleSub(ctx, googleSub); err == nil {
		return existing, nil
	}
	if existing, err := s.GetUserByEmail(ctx, email); err == nil {
		if err := s.LinkGoogleIdentity(ctx, existing.ID, googleSub, avatarURL); err != nil {
			return nil, err
		}
		return s.GetUser(ctx, existing.ID)
	}
	// Mint a fresh user
	id, err := newUserID()
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = email
	}
	u := &User{
		ID: id, Name: name, Role: "member",
		Email:     strings.ToLower(strings.TrimSpace(email)),
		GoogleSub: googleSub,
		AvatarURL: avatarURL,
	}
	if err := s.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}

// newUserID returns a fresh user identifier ("u-<8 hex>"). Distinct from
// existing manual IDs (kept short to avoid clobbering migration / admin
// scripts that use string IDs like "admin" or "me").
func newUserID() (string, error) {
	var b [4]byte
	if _, err := randRead(b[:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return "u-" + hex.EncodeToString(b[:]), nil
}

// CreateToken issues a new long-lived API token (token_type='api'). Use
// CreateSessionToken for short-lived browser sessions. Returns the
// *plain* token exactly once.
func (s *Store) CreateToken(ctx context.Context, userID, name string, scopes []string, expiresAt *time.Time) (string, error) {
	return s.createToken(ctx, userID, name, scopes, expiresAt, "api")
}

// CreateSessionToken issues a short-lived browser session as a
// token_type='session' row in api_tokens. Same hash + lookup as
// regular tokens so all authz code keeps working unchanged.
func (s *Store) CreateSessionToken(ctx context.Context, userID, name string, scopes []string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	expiry := time.Now().Add(ttl)
	return s.createToken(ctx, userID, name, scopes, &expiry, "session")
}

func (s *Store) createToken(ctx context.Context, userID, name string, scopes []string, expiresAt *time.Time, tokenType string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: name required", ErrInvalidInput)
	}
	if len(scopes) == 0 {
		return "", fmt.Errorf("%w: scopes required", ErrInvalidInput)
	}
	plain, err := GenerateToken()
	if err != nil {
		return "", err
	}
	hash := HashToken(plain)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO api_tokens(token_hash, user_id, name, scopes, expires_at, token_type)
		VALUES (?, ?, ?, ?, ?, ?)`,
		hash, nullable(userID), name, joinScopes(scopes), nullableTime(expiresAt), tokenType)
	if err != nil {
		return "", translateErr(err)
	}
	return plain, nil
}

// RevokeToken deletes a token by its plain form. Used for logout.
func (s *Store) RevokeToken(ctx context.Context, plain string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM api_tokens WHERE token_hash = ?`, HashToken(plain))
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// LookupToken finds an active token by its plain form. Bumps last_used_at
// on success. Returns ErrNotFound if absent or expired.
func (s *Store) LookupToken(ctx context.Context, plain string) (*APIToken, error) {
	hash := HashToken(plain)
	row := s.db.QueryRowContext(ctx, `
		SELECT token_hash, COALESCE(user_id,''), name, scopes,
		       COALESCE(token_type,'api'),
		       created_at, expires_at, last_used_at
		FROM api_tokens WHERE token_hash = ?`, hash)
	var (
		t          APIToken
		scopesCSV  string
		expN, luN  nullTimeBox
	)
	if err := row.Scan(&t.TokenHash, &t.UserID, &t.Name, &scopesCSV,
		&t.TokenType, &t.CreatedAt, &expN, &luN); err != nil {
		return nil, translateErr(err)
	}
	if expN.Valid {
		ts := expN.Time
		t.ExpiresAt = &ts
	}
	if luN.Valid {
		ts := luN.Time
		t.LastUsedAt = &ts
	}
	t.Scopes = splitScopes(scopesCSV)

	if t.ExpiresAt != nil && t.ExpiresAt.Before(time.Now()) {
		return nil, ErrNotFound
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET last_used_at = CURRENT_TIMESTAMP WHERE token_hash = ?`, hash); err != nil {
		return nil, err
	}
	return &t, nil
}

// HasScope reports whether the supplied scopes satisfy `required`. "admin"
// implicitly grants every scope.
func HasScope(have []string, required string) bool {
	for _, s := range have {
		if s == required || s == "admin" {
			return true
		}
	}
	return false
}

func joinScopes(scopes []string) string {
	out := make([]string, 0, len(scopes))
	seen := map[string]bool{}
	for _, s := range scopes {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func splitScopes(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// nullTimeBox is a scanner that accepts nil / time.Time / SQLite's text
// timestamp formats and reports validity. Equivalent to sql.NullTime with
// looser parsing.
type nullTimeBox struct {
	Valid bool
	Time  time.Time
}

func (n *nullTimeBox) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		n.Valid = false
	case time.Time:
		n.Valid = true
		n.Time = v
	case []byte:
		return n.fromString(string(v))
	case string:
		return n.fromString(v)
	}
	return nil
}

func (n *nullTimeBox) fromString(s string) error {
	// SQLite emits aggregate-of-TIMESTAMP values as TEXT in a few flavours
	// depending on whether sub-second precision and timezone offsets were
	// stored. We accept them all so the EntrySignals view (which MAX()es
	// usage_cases.retrieved_at) round-trips cleanly.
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			n.Valid = true
			n.Time = t
			return nil
		}
	}
	return fmt.Errorf("nullTimeBox: cannot parse %q", s)
}
