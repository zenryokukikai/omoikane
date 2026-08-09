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
}

// CreateWebhook registers a subscription and mints its secret. The
// returned struct is the ONLY place the secret is ever exposed.
func (s *Store) CreateWebhook(ctx context.Context, url string, eventTypes []string, createdBy string) (*WebhookSubscription, error) {
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
	var sec [16]byte
	if _, err := rand.Read(sec[:]); err != nil {
		return nil, err
	}
	secret := hex.EncodeToString(sec[:])
	id := newLibrarianID("wh")
	types, _ := json.Marshal(clean)
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO webhook_subscriptions(id, url, event_types, secret, created_by)
		VALUES (?, ?, ?, ?, ?)`, id, url, string(types), secret, createdBy); err != nil {
		return nil, translateErr(err)
	}
	return &WebhookSubscription{ID: id, URL: url, EventTypes: clean,
		Secret: secret, Active: true, CreatedBy: createdBy, CreatedAt: time.Now().UTC()}, nil
}

func scanWebhook(sc scanner) (*WebhookSubscription, error) {
	var w WebhookSubscription
	var types string
	if err := sc.Scan(&w.ID, &w.URL, &types, &w.Active, &w.CreatedBy, &w.CreatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(types), &w.EventTypes)
	return &w, nil
}

// ListWebhooks returns all subscriptions, newest first, secrets omitted.
func (s *Store) ListWebhooks(ctx context.Context) ([]*WebhookSubscription, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, url, event_types, active, created_by, created_at
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

// DeliveryTarget carries what the dispatcher needs: url + secret.
type DeliveryTarget struct {
	ID     string
	URL    string
	Secret string
}

// ListActiveWebhooksForEvent returns delivery targets subscribed to
// eventType. The secret rides along ONLY on this internal path.
func (s *Store) ListActiveWebhooksForEvent(ctx context.Context, eventType string) ([]DeliveryTarget, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, url, secret FROM webhook_subscriptions
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
		if err := rows.Scan(&t.ID, &t.URL, &t.Secret); err != nil {
			return nil, err
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
