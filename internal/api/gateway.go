package api

// External-gateway control surface (issue #104 G3a, C案).
//
// The "gateway" scope marks omoikane's OWN infrastructure token — the
// one omoikane-gate process that relays /talk traffic between the chat
// store and the external gate plane. Trust level: the in-process
// webhook dispatcher (it subscribes to SSE unfiltered and posts on
// behalf of personal librarians and their owners). What the scope
// grants:
//
//   - full space visibility (ResolveVisibleSpaces — same nil contract
//     as admin),
//   - author attribution: POST /v1/librarian/chat and
//     POST /v1/events/broadcast honour the request's author_user_id,
//     and the thread-usability check runs AS that user (a thread the
//     stamped user cannot use stays a 404 — fail-closed),
//   - GET /v1/gateway/librarians, the gate binary's connection roster.
//
// It deliberately does NOT widen mayUseThread by itself: a gateway
// call without a stamped author is a user-less token and gets the
// user-less answer (404 on talk threads).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// ThreadGateBinder registers one /talk thread as a gate binding on the
// external admin plane (issue #104 G3a). Implemented by
// opencrab.GateProvisioner; nil on the Handler = gate feature off
// (GATE_ADMIN_URL unset).
type ThreadGateBinder interface {
	EnsureThreadBinding(ctx context.Context, instanceID, threadID string) (bindingID string, err error)
}

// gatewayStampedAuthor returns the author_user_id the request asked to
// attribute, but ONLY when the caller's token carries the LITERAL
// "gateway" scope. Deliberately not store.HasScope: the admin wildcard
// must not silently extend to author attribution — an admin token
// posting chat keeps today's behaviour (field ignored, message
// attributed to the token's own user). Every other scope: "" (field
// ignored exactly as before the gateway existed).
func gatewayStampedAuthor(r *http.Request, requested string) string {
	if requested == "" {
		return ""
	}
	tok := auth.FromContext(r.Context())
	if tok == nil {
		return ""
	}
	for _, s := range tok.Scopes {
		if s == "gateway" {
			return requested
		}
	}
	return ""
}

// GET /v1/gateway/librarians — the gate binary's connection roster:
// every ACTIVE personal librarian with its owner and registered gate
// instance id ("" = instance not registered yet; the gate binary skips
// those). RequireScope("gateway") gates the route — any other token
// gets the uniform 403 (pinned in gateway_test.go).
func (h *Handler) gatewayListLibrarians(w http.ResponseWriter, r *http.Request) {
	list, err := h.Store.ListActiveUserLibrarians(httpCtx(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	type row struct {
		UserID         string `json:"user_id"`
		AgentID        string `json:"agent_id"`
		Name           string `json:"name"`
		GateInstanceID string `json:"gate_instance_id"`
	}
	out := make([]row, 0, len(list))
	for _, ul := range list {
		out = append(out, row{
			UserID: ul.UserID, AgentID: ul.AgentID,
			Name: ul.Name, GateInstanceID: ul.GateInstanceID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"librarians": out})
}

// GET /v1/gateway/threads/{threadID}/cursor — the reconnect replay
// cursor for one /talk thread: the newest message id the gateway
// confirmed dispatching (last_sent_message_id, "" = nothing sent yet).
// 404 when the thread has no gate binding row — same shape as the
// store's ErrNotFound, and the gate reads that as "replay from the
// beginning" (origin idempotency keeps the overlap harmless).
// RequireScope("gateway") gates the route; every other token gets the
// uniform 403 (pinned in gateway_test.go).
func (h *Handler) gatewayGetThreadCursor(w http.ResponseWriter, r *http.Request) {
	threadID := chi.URLParam(r, "threadID")
	b, err := h.Store.GetTalkGateBinding(httpCtx(r), threadID)
	if err != nil {
		writeStoreError(w, err) // ErrNotFound → 404
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"thread_id":            threadID,
		"last_sent_message_id": b.LastSentMessageID,
	})
}

type gatewayCursorRequest struct {
	MessageID string `json:"message_id"`
}

// PUT /v1/gateway/threads/{threadID}/cursor — advances the replay
// cursor after the gate confirmed dispatching message_id. The id must
// be a real message OF THIS THREAD: an arbitrary or cross-thread id is
// rejected (400), never silently stored, so the cursor can only ever
// trail this thread's own history. 404 when the thread has no binding
// row (the cursor only trails an existing binding).
// RequireScope("gateway") gates the route.
func (h *Handler) gatewaySetThreadCursor(w http.ResponseWriter, r *http.Request) {
	threadID := chi.URLParam(r, "threadID")
	var req gatewayCursorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.MessageID == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "message_id is required", nil)
		return
	}
	// Membership check: the store's SetTalkGateBindingCursor does not
	// validate that the id belongs to the thread, so guard it here rather
	// than trust the caller. A missing or cross-thread id is a 400 — the
	// cursor never points outside its own thread's history.
	msg, err := h.Store.GetChatMessage(httpCtx(r), req.MessageID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "message_id does not belong to this thread", nil)
			return
		}
		writeStoreError(w, err)
		return
	}
	if msg.ThreadID != threadID {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "message_id does not belong to this thread", nil)
		return
	}
	if err := h.Store.SetTalkGateBindingCursor(httpCtx(r), threadID, req.MessageID); err != nil {
		writeStoreError(w, err) // ErrNotFound (no binding) → 404
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"thread_id":            threadID,
		"last_sent_message_id": req.MessageID,
	})
}

// talkGateBindTimeout caps the admin-plane PUT during thread creation.
// The call is synchronous BY CHOICE: the first message dispatch follows
// the thread-creation response, so completing the PUT before responding
// guarantees the binding exists by then; the short cap keeps a slow
// admin plane from stalling thread creation for more than a moment.
const talkGateBindTimeout = 5 * time.Second

// bindTalkThreadGate registers a freshly created /talk thread as a gate
// binding when the creator has an ACTIVE personal librarian with a
// registered gate instance. Best-effort by design: ANY failure logs and
// returns — thread creation must never fail on the gate leg (the /talk
// flow works without a gate; delivery falls back to the current
// webhook/dispatch path).
func (h *Handler) bindTalkThreadGate(ctx context.Context, createdBy, threadID string) {
	if h.GateBinder == nil || createdBy == "" {
		return
	}
	ul, err := h.Store.GetUserLibrarian(ctx, createdBy)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			h.Logger.Warn("talk gate binding: librarian lookup failed",
				"thread_id", threadID, "user_id", createdBy, "err", err)
		}
		return // no personal librarian: nothing to bind
	}
	if ul.Status != "active" || ul.GateInstanceID == "" {
		return // librarian inactive, or no gate instance registered
	}
	bctx, cancel := context.WithTimeout(ctx, talkGateBindTimeout)
	defer cancel()
	bindingID, err := h.GateBinder.EnsureThreadBinding(bctx, ul.GateInstanceID, threadID)
	if err != nil {
		h.Logger.Warn("talk gate binding failed; thread continues without gate",
			"thread_id", threadID, "instance_id", ul.GateInstanceID, "err", err)
		return
	}
	if err := h.Store.PutTalkGateBinding(ctx, threadID, bindingID, ul.GateInstanceID); err != nil {
		h.Logger.Warn("talk gate binding row write failed",
			"thread_id", threadID, "binding_id", bindingID, "err", err)
	}
}
