package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
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
			if e.Type == "chat.message" {
				if !chatEventFromHuman(e.Data) {
					continue
				}
				// Personal-librarian routing (issue #73 slice B): a human
				// /talk message whose thread owner has an active personal
				// librarian goes to that agent on the runtime INSTEAD of
				// the webhook-subscribed default responder — two answering
				// agents on one thread is never wanted.
				if h.routeTalkToPersonalLibrarian(e.Data) {
					continue
				}
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

// TalkDispatcher delivers one /talk message to a personal-librarian
// agent on an external runtime and returns the agent's reply text (the
// runtime's messages API is synchronous over the whole turn). Implemented
// by *opencrab.Client; an interface so tests can stand in a fake runtime.
//
// ownerUserID is the kb user id of the librarian's owner — the identity
// the runtime resolves the caller by (issue #137). It is per librarian:
// the same value provisioning wrote into that agent's trust row.
type TalkDispatcher interface {
	DispatchTalk(ctx context.Context, agentID, ownerUserID, content string) (reply string, err error)
}

// talkDispatchTimeout bounds one runtime dispatch. The messages API is
// synchronous over the agent's whole LLM turn, so this is a turn
// ceiling, not a request latency: generous on purpose (the /talk UI's
// own pending-stale cutoff is 5 minutes).
const talkDispatchTimeout = 5 * time.Minute

// routeTalkToPersonalLibrarian diverts a human /talk chat.message to the
// thread owner's personal librarian. Reports true when the message was
// claimed by this route — the caller must then NOT deliver it to webhook
// subscriptions, or the user would get two competing replies.
//
// The claim decision is data-driven and fails open toward the default
// responder: dispatcher unconfigured, non-talk thread, owner without an
// ACTIVE librarian row, or a lookup error all fall through to the
// webhook path (previous behaviour, and the safe direction — the user
// still gets an answer). Once claimed, delivery is fire-and-forget with
// a timeout; a runtime failure is logged and the message is NOT
// re-delivered to webhooks — the same at-most-once contract as webhook
// delivery itself (consumers reconcile via the list APIs).
func (h *Handler) routeTalkToPersonalLibrarian(data any) bool {
	if h.TalkDispatch == nil {
		return false
	}
	m, ok := data.(map[string]any)
	if !ok {
		return false
	}
	intent, _ := m["thread_intent"].(string)
	threadID, _ := m["thread_id"].(string)
	owner, _ := m["thread_created_by"].(string)
	if intent != "talk" || threadID == "" || owner == "" {
		return false
	}
	ul, err := h.Store.GetUserLibrarian(context.Background(), owner)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			h.Logger.Warn("talk: librarian lookup failed; using webhook route",
				"owner", owner, "thread", threadID, "err", err)
		}
		return false
	}
	if ul.Status != "active" || ul.AgentID == "" {
		return false
	}
	// Gateway-path claim (issue #104 cutover). This is the SINGLE
	// contract point that selects the delivery path for a /talk
	// message; scoping is per-user AND per-thread, which gives a
	// gradual cutover:
	//
	//   - per-user: gate_instance_id set on the librarian row = this
	//     librarian runs behind the external gate;
	//   - per-thread: a talk_gate_bindings row = the gateway carries
	//     this thread's messages (SSE → said). Pre-cutover threads
	//     have no binding row and keep REST dispatch; threads created
	//     after the instance registered go through the gateway.
	//
	// Claimed = return true: neither the REST dispatch below nor the
	// webhook default responder may fire — either would answer on top
	// of the gateway's own delivery (the double-reply this guard
	// exists to prevent; returning false would hand the message to
	// the default responder, which is still a second answer). The
	// gate-down case is safe: the message stays in librarian_chat and
	// the gate's on-bind replay (internal/gate/runtime/instance.go,
	// OnBind → replay from the stored cursor) delivers it once the
	// binding reconnects — late, never dropped. GATE_TALK_REST_FORCE
	// (h.GateTalkRESTForce) is the emergency kill switch: it skips
	// this claim entirely, reverting every thread to REST dispatch
	// without touching the DB or restarting opencrab.
	if !h.GateTalkRESTForce && ul.GateInstanceID != "" {
		if _, err := h.Store.GetTalkGateBinding(context.Background(), threadID); err == nil {
			h.Logger.Info("talk claimed by gateway path",
				"thread", threadID, "agent_id", ul.AgentID,
				"gate_instance_id", ul.GateInstanceID)
			return true
		}
		// Any lookup error — ErrNotFound (unbound/pre-cutover thread)
		// included — falls through to REST dispatch: the safe
		// direction, the user still gets exactly one answer.
	}
	content, _ := m["content"].(string)
	title, _ := m["thread_title"].(string)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), talkDispatchTimeout)
		defer cancel()
		// Caller identity = the librarian's owner (the thread owner
		// whose row we just read): the value provisioning wrote into
		// its trust row, so the runtime resolves this dispatch as the
		// owner and the owner-gated tools stay available (issue #137).
		reply, err := h.TalkDispatch.DispatchTalk(ctx, ul.AgentID, ul.UserID,
			talkDispatchContent(title, content))
		if err != nil {
			// Log and stop: an error string is never posted as the
			// librarian's words — the thread just stays unanswered
			// (same at-most-once contract as above).
			h.Logger.Warn("talk: personal librarian dispatch failed",
				"agent_id", ul.AgentID, "thread", threadID, "err", err)
			return
		}
		h.deliverTalkReply(threadID, title, owner, reply)
	}()
	return true
}

// deliverTalkReply posts one REST-dispatch reply into its /talk thread
// as the librarian and emits the terminal chat.status broadcast — the
// same delivery contract the gateway lane provides (the gate posts the
// say, then broadcasts done). Since #132 the agent's instructions carry
// no posting recipe, so on the REST fallback path reply delivery is the
// SERVER's responsibility; before #134 the reply was silently dropped
// here.
//
// Suppression parity with the gateway lane: an empty reply or the exact
// NO_REPLY sentinel (trimmed match — opencrab's own `trim() ==
// "NO_REPLY"` rule) posts nothing. On the gateway lane such a turn
// produces no say and the user sees silence; the fallback must mean the
// same silence, not a literal "NO_REPLY" message.
func (h *Handler) deliverTalkReply(threadID, title, owner, reply string) {
	if t := strings.TrimSpace(reply); t == "" || t == "NO_REPLY" {
		return
	}
	// Own deadline: the dispatch context above may have spent most of
	// the 5-minute turn budget, and an earned reply must not be dropped
	// over that.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Thread context reconstructed from the routed event's own fields
	// (they were read off the thread row when the human message was
	// posted); intent is "talk" by the routing guard above.
	th := &store.ChatThread{ThreadID: threadID, Intent: "talk", CreatedBy: owner, Title: title}
	// Same author semantics as the gateway lane's PostAssistantReply
	// (internal/gate/runtime/kb.go): role assistant, attributed to the
	// thread owner, intent observation — via the one chat write
	// contract (storeChatMessage), never a second shape.
	if _, err := h.storeChatMessage(ctx, th, &store.ChatMessage{
		ThreadID: threadID, AuthorRole: "assistant", AuthorUserID: owner,
		Intent: "observation", Content: reply,
	}); err != nil {
		h.Logger.Warn("talk: storing REST-fallback reply failed",
			"thread", threadID, "err", err)
		return
	}
	// Terminal status: the /talk UI clears its pending indicator on
	// {thread_id, done:true} — the same event the gateway lane
	// broadcasts via POST /v1/events/broadcast (see broadcastEvent).
	h.Events.Publish(Event{Type: "chat.status",
		Data:    map[string]any{"thread_id": threadID, "done": true},
		SpaceID: threadEventSpace(th)})
}

// talkDispatchContent frames a human /talk message for the runtime's
// messages API. The messages endpoint is synchronous: the response body
// of this very call is the reply, and the server delivers it into the
// thread itself (deliverTalkReply, issue #134) — so the framing tells
// the agent exactly that, and deliberately hands over NO thread_id and
// no posting recipe: the REST-era recipe is gone since #132, and an
// agent that tried to post its own reply would double-post.
func talkDispatchContent(title, body string) string {
	var b strings.Builder
	b.WriteString("[omoikane /talk] 利用者から新しいメッセージが届きました。" +
		"この依頼へのあなたの応答本文が、そのまま利用者への返信として届けられます" +
		"(自分で投稿する必要はありません)。" +
		"返信が不要と判断した場合は NO_REPLY とだけ返してください。\n")
	if title != "" {
		b.WriteString("スレッド題: " + title + "\n")
	}
	b.WriteString("\n")
	b.WriteString(body)
	return b.String()
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
