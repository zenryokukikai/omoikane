package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/auth"
)

// Webhook subscriptions (issue #33): push events to external agent
// runtimes (first consumer: opencrab's POST /api/hooks/omoikane).
// Delivery is at-most-once — the stream is a latency optimisation and
// consumers reconcile via the list APIs, same contract as SSE.

// POST /v1/admin/webhooks {url, event_types}
func (h *Handler) createWebhook(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken, "token required", nil)
		return
	}
	var req struct {
		URL        string   `json:"url"`
		EventTypes []string `json:"event_types"`
		// SpaceScope omitted/null = deliver events from every space
		// (the pre-slice-4 contract; existing subscriptions are trusted
		// infrastructure). A list restricts delivery to those spaces.
		SpaceScope []string `json:"space_scope,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	sub, err := h.Store.CreateWebhook(httpCtx(r), req.URL, req.EventTypes, req.SpaceScope, tok.UserID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// The ONE response that carries the secret.
	writeJSON(w, http.StatusCreated, sub)
}

// GET /v1/admin/webhooks — secrets omitted.
func (h *Handler) listWebhooks(w http.ResponseWriter, r *http.Request) {
	subs, err := h.Store.ListWebhooks(httpCtx(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": subs, "total": len(subs)})
}

// PATCH /v1/admin/webhooks/{id} {active}
func (h *Handler) patchWebhook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Active *bool `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Active == nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, "active (bool) required", nil)
		return
	}
	if err := h.Store.SetWebhookActive(httpCtx(r), chi.URLParam(r, "id"), *req.Active); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": chi.URLParam(r, "id"), "active": *req.Active})
}

// DELETE /v1/admin/webhooks/{id}
func (h *Handler) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	if err := h.Store.DeleteWebhook(httpCtx(r), chi.URLParam(r, "id")); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": chi.URLParam(r, "id"), "deleted": true})
}

// startWebhookDispatcher subscribes to the EventBus and delivers each
// event to matching subscriptions. Runs for the process lifetime.
// Slow consumers cannot block the bus (bounded subscriber buffer, the
// bus drops on overflow), and delivery failures never propagate.
func (h *Handler) startWebhookDispatcher() {
	// nil predicate = the trusted in-process subscription: the
	// dispatcher must observe every event because per-subscription
	// space scoping happens below (an unscoped subscription — the
	// existing /talk responder — receives all spaces by contract).
	events, _ := h.Events.Subscribe(nil)
	client := &http.Client{Timeout: 5 * time.Second}
	go func() {
		for e := range events {
			// chat.message: deliver human speech only. Webhook consumers
			// are agent runtimes replying into these threads — delivering
			// an agent's own reply back to it makes the runtime spend an
			// LLM turn just to conclude "not human, ignore" (#39). Echo
			// suppression is this pipe's guarantee, not the consumer's
			// self-restraint. The /talk UI still sees every message via
			// SSE — this filter is webhook-only.
			if e.Type == "chat.message" && !chatEventFromHuman(e.Data) {
				continue
			}
			targets, err := h.Store.ListActiveWebhooksForEvent(context.Background(), e.Type)
			if err != nil {
				h.Logger.Warn("webhook: subscription lookup failed", "err", err)
				continue
			}
			if len(targets) == 0 {
				continue
			}
			body, err := json.Marshal(map[string]any{
				"type":         e.Type,
				"data":         e.Data,
				"delivered_at": time.Now().UTC().Format(time.RFC3339),
			})
			if err != nil {
				continue
			}
			for _, t := range targets {
				// Space scoping (slice 4): scoped subscriptions receive
				// only events from their listed spaces; unstamped events
				// never reach a scoped subscription (fail-closed).
				if !t.AllowsSpace(e.SpaceID) {
					continue
				}
				go deliverWebhook(client, h.Logger, t.URL, t.ID, e.Type, body, signBody(t.Secret, body))
			}
		}
	}()
}

// chatEventFromHuman reports whether a chat.message event's payload says
// the author is human. Unknown shapes fail closed (not delivered): every
// publisher of chat.message sets author_role, so a missing field means a
// payload this filter doesn't understand, and guessing "human" would
// reopen the echo loop #39 exists to close.
func chatEventFromHuman(data any) bool {
	m, ok := data.(map[string]any)
	if !ok {
		return false
	}
	role, _ := m["author_role"].(string)
	return role == "human"
}

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// deliverWebhook posts with up to 3 retries (1s/2s/4s). At-most-once:
// exhausting retries drops the event with a log line.
func deliverWebhook(client *http.Client, logger *slog.Logger, url, subID, eventType string, body []byte, signature string) {
	backoff := time.Second
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Omoikane-Event", eventType)
		req.Header.Set("X-Omoikane-Signature", signature)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 300 {
				return
			}
			// 4xx won't heal on retry; log and stop.
			if resp.StatusCode < 500 {
				logger.Warn("webhook: rejected", "sub", subID, "event", eventType, "status", resp.StatusCode)
				return
			}
		}
		if attempt < 3 {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	logger.Warn("webhook: delivery failed after retries", "sub", subID, "event", eventType)
}
