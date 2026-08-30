package api

// Librarian chat: /v1/librarian/threads + /v1/librarian/chat.
//
// Thread ACL (mayUseThread / requireUsableThread), thread lifecycle
// (open / close / list) and message posting / listing (incl. the
// long-poll cursor mode). requireUsableThread is also called from
// events.go, so it stays package-level here.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// ============================================================
// /v1/librarian/chat + threads
// ============================================================

// mayUseThread reports whether the request's principal may read or
// write the given thread (issue #60 slice 4). Non-talk threads are the
// shared librarian-coordination room: everyone. intent=talk threads
// are personal conversations, allowed only to:
//
//   - the owner (created_by),
//   - the admin scope (the one admin-visibility contract), and
//   - when agentOK: agent users (users.role == "agent"). The /talk
//     responder runtime reads a thread's history, posts its answers,
//     and streams chat.status progress with an agent token that is
//     neither the owner nor admin — without this exception the
//     deployed response path breaks. The exception covers ONLY that
//     response path (messages / chat / broadcast); closing a thread is
//     not part of it, so close passes agentOK=false. Phase 2's
//     space-scoped agent tokens will narrow this to designated
//     responders; today every agent user is trusted infrastructure.
//
// actor, when non-empty, is the gateway-stamped author the ownership
// check runs AS (issue #104 G3a): the caller holds the literal
// "gateway" scope (validated by gatewayStampedAuthor — the only
// producer of a non-empty actor) and relays on behalf of that user, so
// a thread the stamped user cannot use stays a 404 exactly as if the
// user had called directly.
//
// INVARIANT (G3a adversarial review, MEDIUM): a gateway stamp confers
// EXACTLY the stamped user's ownership, never agent authority — so a
// gateway token's own UserID/role is irrelevant to stamped calls. When
// actor != "" the only question asked is `actor == th.CreatedBy`; the
// agent exception below is NOT consulted. This is fail-closed by
// construction: even a gateway token mis-minted with a UserID that
// resolves to an agent-role user cannot impersonate an arbitrary user
// into an arbitrary talk thread, because the stamp path never reaches
// the role check. Only the non-stamped path (actor == "") — where the
// caller acts as itself — may use the agent exception, keyed on the
// CALLER's own role.
//
// Callers translate false into 404 — for outsiders a foreign talk
// thread must be indistinguishable from a missing one.
func (h *Handler) mayUseThread(r *http.Request, th *store.ChatThread, agentOK bool, actor string) bool {
	if th.Intent != "talk" {
		return true
	}
	tok := auth.FromContext(r.Context())
	if tok == nil {
		return false
	}
	if store.HasScope(tok.Scopes, "admin") {
		return true
	}
	// Gateway-stamped relay: authority is exactly the stamped user's
	// ownership. The agent exception is deliberately unreachable here so
	// the gateway token's own identity can never widen the stamp.
	if actor != "" {
		return actor == th.CreatedBy
	}
	// Non-stamped path: the caller acts as itself.
	if tok.UserID == "" {
		return false
	}
	if tok.UserID == th.CreatedBy {
		return true
	}
	if !agentOK {
		return false
	}
	u, err := h.Store.GetUser(httpCtx(r), tok.UserID)
	return err == nil && u.Role == "agent"
}

// requireUsableThread loads the thread and enforces mayUseThread,
// writing the error response itself and returning nil on failure. The
// point of the single helper: "no such thread" and "hidden talk
// thread" produce ONE byte-identical 404 — the third-party review of
// this slice caught that the two paths' differing message strings
// ("store: not found" vs "thread not found") were themselves an
// existence oracle. Every thread-addressed route (messages / close /
// chat post / broadcast) must come through here, never through its own
// GetThread+writeStoreError pair.
func (h *Handler) requireUsableThread(w http.ResponseWriter, r *http.Request, threadID string, agentOK bool, actor string) *store.ChatThread {
	th, err := h.Store.GetThread(httpCtx(r), threadID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeStoreError(w, err)
		return nil
	}
	if err != nil || !h.mayUseThread(r, th, agentOK, actor) {
		writeError(w, http.StatusNotFound, CodeNotFound, "thread not found", nil)
		return nil
	}
	return th
}

type chatThreadRequest struct {
	ThreadID       string `json:"thread_id,omitempty"`
	Title          string `json:"title,omitempty"`
	Intent         string `json:"intent,omitempty"`
	Summary        string `json:"summary,omitempty"`
	RelatedEntries string `json:"related_entries,omitempty"`
	Metadata       string `json:"metadata,omitempty"`
}

func (h *Handler) chatOpenThread(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	var req chatThreadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	// Ownership from the auth context, never the body (same authority
	// rule as ChatMessage.AuthorUserID).
	var createdBy string
	if tok := auth.FromContext(r.Context()); tok != nil {
		createdBy = tok.UserID
	}
	id, err := h.Store.OpenThread(httpCtx(r), &store.ChatThread{
		ThreadID: req.ThreadID, Title: req.Title, Intent: req.Intent,
		Summary: req.Summary, RelatedEntries: req.RelatedEntries,
		Metadata: req.Metadata, CreatedBy: createdBy,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Gate binding (issue #104 G3a): a fresh /talk thread is registered
	// on the external gate plane before we respond, so the binding
	// exists by the time the first message dispatch fires. Best-effort —
	// see bindTalkThreadGate.
	if req.Intent == "talk" {
		h.bindTalkThreadGate(httpCtx(r), createdBy, id)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"thread_id": id})
}

type chatCloseRequest struct {
	Summary string `json:"summary,omitempty"`
}

func (h *Handler) chatCloseThread(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	id := chi.URLParam(r, "id")
	var req chatCloseRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
			return
		}
	}
	// Same gate as posting, minus the agent exception: closing someone
	// else's talk thread is a write into their private conversation and
	// the responder never needs it (owner and admin only; 404, no
	// oracle).
	if h.requireUsableThread(w, r, id, false, "") == nil {
		return
	}
	if err := h.Store.CloseThread(httpCtx(r), id, req.Summary); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) chatListThreads(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	// ?mine=1 narrows to threads the caller opened (per-user chat
	// history). Ownership comes from the token, not a parameter.
	createdBy := ""
	if r.URL.Query().Get("mine") == "1" {
		if tok := auth.FromContext(r.Context()); tok != nil {
			createdBy = tok.UserID
		}
	}
	list, err := h.Store.ListThreads(httpCtx(r), status, createdBy, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"threads": list})
}

type chatPostRequest struct {
	ThreadID   string `json:"thread_id,omitempty"`
	AuthorRole string `json:"author_role"`
	// AuthorUserID is honoured ONLY under the literal "gateway" scope
	// (issue #104 G3a C案) — the infra token relaying for personal
	// librarians and their owners. Every other caller: ignored, the
	// author stays the token's own user (server-side authority).
	AuthorUserID     string `json:"author_user_id,omitempty"`
	AuthorInstanceID string `json:"author_instance_id,omitempty"`
	ReplyTo          string `json:"reply_to,omitempty"`
	Mentions         string `json:"mentions,omitempty"`
	Intent           string `json:"intent,omitempty"`
	Content          string `json:"content"`
	RelatedEntries   string `json:"related_entries,omitempty"`
	InputTokens      int    `json:"input_tokens,omitempty"`
	OutputTokens     int    `json:"output_tokens,omitempty"`
}

func (h *Handler) chatPost(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	var req chatPostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	// author_user_id is server-side authority: we pull it from the
	// auth context, never from the request body. This means a reader
	// can trust the link from "this message" → "this profile" — no
	// way for a client to impersonate someone else here. The ONE
	// exception is the gateway infra token (issue #104 G3a): under the
	// literal "gateway" scope the requested author_user_id is accepted
	// AND the thread check below runs as that user, so the gateway can
	// only reach threads its stamped owner could reach (fail-closed).
	var authorUserID string
	if tok := auth.FromContext(r.Context()); tok != nil {
		authorUserID = tok.UserID
	}
	actor := gatewayStampedAuthor(r, req.AuthorUserID)
	if actor != "" {
		authorUserID = actor
	}
	// Resolve the target thread up front (slice 4): posting into
	// someone else's talk thread — or a thread that does not exist —
	// is one indistinguishable 404. (Thread-less messages never could
	// reference a thread; the librarian_chat FK already rejected
	// dangling ids, so requiring the row here changes no working flow.)
	var thread *store.ChatThread
	if req.ThreadID != "" {
		if thread = h.requireUsableThread(w, r, req.ThreadID, true, actor); thread == nil {
			return
		}
	}
	id, err := h.storeChatMessage(httpCtx(r), thread, &store.ChatMessage{
		ThreadID: req.ThreadID, AuthorRole: req.AuthorRole,
		AuthorInstanceID: req.AuthorInstanceID,
		AuthorUserID:     authorUserID,
		ReplyTo:          req.ReplyTo,
		Mentions:         req.Mentions, Intent: req.Intent, Content: req.Content,
		RelatedEntries: req.RelatedEntries,
		InputTokens:    req.InputTokens, OutputTokens: req.OutputTokens,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// storeChatMessage is the ONE librarian-chat write contract: persist
// the message row, then push the chat.message event to SSE listeners
// (chat responders, live frontends). Thread context rides along so
// listeners can route without a round-trip; the event is delivered
// under the thread's visibility space — internal for coordination
// threads, the owner's personal space for talk threads (see
// threadEventSpace / the Event doc comment). thread == nil (a
// thread-less message) stores the row without an event, as before.
//
// Both writers come through here so row + event stay one shape: the
// HTTP handler above (chatPost — every external poster, the gateway's
// PostAssistantReply included) and the server-side /talk REST-fallback
// reply delivery (webhooks.go deliverTalkReply, issue #134).
func (h *Handler) storeChatMessage(ctx context.Context, thread *store.ChatThread, m *store.ChatMessage) (string, error) {
	id, err := h.Store.PostChatMessage(ctx, m)
	if err != nil {
		return "", err
	}
	if h.Events != nil && thread != nil {
		ev := map[string]any{
			"id": id, "thread_id": m.ThreadID,
			"author_user_id": m.AuthorUserID, "author_role": m.AuthorRole,
			"content": m.Content, "intent": m.Intent,
			"thread_intent":     thread.Intent,
			"thread_created_by": thread.CreatedBy,
			"thread_title":      thread.Title,
		}
		h.Events.Publish(Event{Type: "chat.message", Data: ev, SpaceID: threadEventSpace(thread)})
	}
	return id, nil
}

// chatList serves GET /v1/librarian/threads/{id}/messages.
//
// Plain mode (`?limit=N`): returns the first N messages in the
// thread, oldest first.
//
// Cursor mode (`?since=<message-id>&limit=N`): returns up to N
// messages newer than the supplied message. Empty list when there's
// nothing newer.
//
// Long-poll mode (`?since=<message-id>&wait=30s`): if cursor-mode
// would return empty, the handler holds the connection for up to
// `wait` seconds, re-checking the store roughly every second. As
// soon as new messages appear the handler flushes and returns.
// This lets agents implement pseudo-realtime ping-pong without
// burning request volume on tight polling loops.
//
// `wait` is capped at 5 minutes to avoid runaway client behaviour
// pinning server resources. Context cancellation (client
// disconnect) terminates the wait immediately.
func (h *Handler) chatList(w http.ResponseWriter, r *http.Request) {
	threadID := chi.URLParam(r, "id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	sinceID := r.URL.Query().Get("since")
	waitStr := r.URL.Query().Get("wait")

	ctx := httpCtx(r)

	// Slice 4: a foreign talk thread and a nonexistent thread are one
	// indistinguishable 404. (Previously an unknown thread returned an
	// empty 200 — that would have become an existence oracle next to
	// the 404 for hidden threads.)
	//
	// Gateway replay (issue #104 G3c): a read is authenticated by query
	// (never a body), so the gateway stamp arrives as ?author_user_id=.
	// gatewayStampedAuthor honours it ONLY under the literal "gateway"
	// scope; every other caller gets "" and the param is ignored exactly
	// as before. The stamp flows through the same actor param as the
	// write paths, so the ownership check runs as the stamped owner and a
	// foreign thread stays a 404.
	actor := gatewayStampedAuthor(r, r.URL.Query().Get("author_user_id"))
	if h.requireUsableThread(w, r, threadID, true, actor) == nil {
		return
	}

	// Resolve `since` to a timestamp (if it points at a real msg).
	// Unknown id → treat as no cursor (start from beginning).
	var sinceTS time.Time
	if sinceID != "" {
		if m, err := h.Store.GetChatMessage(ctx, sinceID); err == nil {
			sinceTS = m.Timestamp
		}
	}

	// Parse wait duration. Cap at 5 minutes. Zero = no long-poll.
	var waitUntil time.Time
	if waitStr != "" {
		d, err := time.ParseDuration(waitStr)
		if err == nil && d > 0 {
			if d > 5*time.Minute {
				d = 5 * time.Minute
			}
			waitUntil = time.Now().Add(d)
		}
	}

	for {
		msgs, err := h.Store.ListChatMessagesSince(ctx, threadID, sinceTS, limit)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if len(msgs) > 0 || time.Now().After(waitUntil) {
			writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
			return
		}
		// No new messages and still inside the wait window. Sleep
		// ~1s then re-check, but bail out on client disconnect.
		select {
		case <-time.After(1 * time.Second):
		case <-ctx.Done():
			// Client gave up. Just return what we have (empty).
			writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
			return
		}
	}
}
