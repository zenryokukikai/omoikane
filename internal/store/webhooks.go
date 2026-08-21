package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// WebhookSubscription is one registered delivery target. Secret is
// only populated on creation — reads return it empty.
type WebhookSubscription struct {
	ID         string    `json:"id"`
	URL        string    `json:"url"`
	EventTypes []string  `json:"event_types"`
	Secret     string    `json:"secret,omitempty"`
	Active     bool      `json:"active"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
	// SpaceScope nil = deliver events from every space (trusted
	// infrastructure — the pre-slice-4 behaviour, kept for existing
	// subscriptions). Non-nil = deliver ONLY events whose SpaceID is
	// listed; events without a space are then dropped (fail-closed).
	SpaceScope []string `json:"space_scope,omitempty"`
}

// CreateWebhook registers a subscription and mints its secret. The
// returned struct is the ONLY place the secret is ever exposed.
// spaceScope nil = all spaces (see WebhookSubscription.SpaceScope).
func (s *Store) CreateWebhook(ctx context.Context, url string, eventTypes []string, spaceScope []string, createdBy string) (*WebhookSubscription, error) {
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("%w: url must be http(s)", ErrInvalidInput)
	}
	clean := make([]string, 0, len(eventTypes))
	for _, t := range eventTypes {
		if t = strings.TrimSpace(t); t != "" {
			clean = append(clean, t)
		}
	}
	if len(clean) == 0 {
		return nil, fmt.Errorf("%w: event_types required", ErrInvalidInput)
	}
	// An explicit empty list is a footgun, not a contract: it would
	// deliver NOTHING while looking like "no restriction". All-spaces
	// delivery is spelled by omitting the field (NULL).
	if spaceScope != nil && len(spaceScope) == 0 {
		return nil, fmt.Errorf(
			"%w: space_scope must not be an empty list (omit the field for all-spaces delivery, or list the spaces to deliver)",
			ErrInvalidInput)
	}
	var sec [16]byte
	if _, err := rand.Read(sec[:]); err != nil {
		return nil, err
	}
	secret := hex.EncodeToString(sec[:])
	id := newLibrarianID("wh")
	types, _ := json.Marshal(clean)
	var scopeVal any // NULL when unscoped
	if spaceScope != nil {
		b, _ := json.Marshal(spaceScope)
		scopeVal = string(b)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO webhook_subscriptions(id, url, event_types, secret, created_by, space_scope)
		VALUES (?, ?, ?, ?, ?, ?)`, id, url, string(types), secret, createdBy, scopeVal); err != nil {
		return nil, translateErr(err)
	}
	return &WebhookSubscription{ID: id, URL: url, EventTypes: clean,
		Secret: secret, Active: true, CreatedBy: createdBy,
		CreatedAt: time.Now().UTC(), SpaceScope: spaceScope}, nil
}

func scanWebhook(sc scanner) (*WebhookSubscription, error) {
	var w WebhookSubscription
	var types string
	var scope sql.NullString
	if err := sc.Scan(&w.ID, &w.URL, &types, &w.Active, &w.CreatedBy, &w.CreatedAt, &scope); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(types), &w.EventTypes)
	if scope.Valid {
		if json.Unmarshal([]byte(scope.String), &w.SpaceScope) == nil && w.SpaceScope == nil {
			// Stored "[]" (or "null"): keep a non-nil empty slice so the
			// scoped-but-empty subscription stays distinguishable from an
			// unscoped one (fail-closed: it delivers nothing).
			w.SpaceScope = []string{}
		}
	}
	return &w, nil
}

// ListWebhooks returns all subscriptions, newest first, secrets omitted.
func (s *Store) ListWebhooks(ctx context.Context) ([]*WebhookSubscription, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, url, event_types, active, created_by, created_at, space_scope
		  FROM webhook_subscriptions ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WebhookSubscription
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// DeliveryTarget carries what the dispatcher needs: url + secret +
// space scope (nil = unscoped, deliver all spaces).
type DeliveryTarget struct {
	ID         string
	URL        string
	Secret     string
	SpaceScope []string
}

// AllowsSpace reports whether an event stamped with spaceID may be
// delivered to this target. Unscoped targets (nil) receive everything —
// the pre-slice-4 trusted-infrastructure contract. Scoped targets only
// receive events from a listed space; an event with NO space is never
// delivered to a scoped target (fail-closed).
func (t DeliveryTarget) AllowsSpace(spaceID string) bool {
	if t.SpaceScope == nil {
		return true
	}
	if spaceID == "" {
		return false
	}
	for _, sp := range t.SpaceScope {
		if sp == spaceID {
			return true
		}
	}
	return false
}

// ListActiveWebhooksForEvent returns delivery targets subscribed to
// eventType. The secret rides along ONLY on this internal path.
func (s *Store) ListActiveWebhooksForEvent(ctx context.Context, eventType string) ([]DeliveryTarget, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, url, secret, space_scope FROM webhook_subscriptions
		 WHERE active = 1
		   AND EXISTS (SELECT 1 FROM json_each(event_types) je WHERE je.value = ?)`,
		eventType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeliveryTarget
	for rows.Next() {
		var t DeliveryTarget
		var scope sql.NullString
		if err := rows.Scan(&t.ID, &t.URL, &t.Secret, &scope); err != nil {
			return nil, err
		}
		if scope.Valid {
			if json.Unmarshal([]byte(scope.String), &t.SpaceScope) == nil && t.SpaceScope == nil {
				t.SpaceScope = []string{} // scoped-but-empty: delivers nothing
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetWebhookActive toggles a subscription.
func (s *Store) SetWebhookActive(ctx context.Context, id string, active bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE webhook_subscriptions SET active = ? WHERE id = ?`, active, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteWebhook removes a subscription.
func (s *Store) DeleteWebhook(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM webhook_subscriptions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

var _ = sql.ErrNoRows
var _ = errors.Is
