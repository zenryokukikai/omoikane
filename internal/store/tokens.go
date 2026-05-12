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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users(id, name, role) VALUES (?, ?, ?)`,
		u.ID, u.Name, u.Role)
	return translateErr(err)
}

func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, role, created_at FROM users WHERE id = ?`, id)
	var u User
	if err := row.Scan(&u.ID, &u.Name, &u.Role, &u.CreatedAt); err != nil {
		return nil, translateErr(err)
	}
	return &u, nil
}

// CreateToken issues a new API token. Returns the *plain* token exactly
// once.
func (s *Store) CreateToken(ctx context.Context, userID, name string, scopes []string, expiresAt *time.Time) (string, error) {
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
		INSERT INTO api_tokens(token_hash, user_id, name, scopes, expires_at)
		VALUES (?, ?, ?, ?, ?)`,
		hash, nullable(userID), name, joinScopes(scopes), nullableTime(expiresAt))
	if err != nil {
		return "", translateErr(err)
	}
	return plain, nil
}

// LookupToken finds an active token by its plain form. Bumps last_used_at
// on success. Returns ErrNotFound if absent or expired.
func (s *Store) LookupToken(ctx context.Context, plain string) (*APIToken, error) {
	hash := HashToken(plain)
	row := s.db.QueryRowContext(ctx, `
		SELECT token_hash, COALESCE(user_id,''), name, scopes,
		       created_at, expires_at, last_used_at
		FROM api_tokens WHERE token_hash = ?`, hash)
	var (
		t          APIToken
		scopesCSV  string
		expN, luN  nullTimeBox
	)
	if err := row.Scan(&t.TokenHash, &t.UserID, &t.Name, &scopesCSV,
		&t.CreatedAt, &expN, &luN); err != nil {
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
