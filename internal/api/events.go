package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// EventBus is a minimal in-process pub/sub for the SSE endpoint. One
// server process (single binary, single instance) means no external
// broker is needed; subscribers that fall behind are dropped rather
// than allowed to block publishers (slow-consumer protection — the
// catch-up path is GET /v1/comments/recent, not an unbounded buffer).
type EventBus struct {
	mu   sync.Mutex
	subs map[chan Event]func(Event) bool
}

// Event is one server-side occurrence pushed to SSE listeners.
//
// SpaceID is the DELIVERY VISIBILITY of the event (issue #60 slice 4),
// never serialised to clients. Every publish site must set it:
//
//   - comment.created  → the entry's space
//   - directive.created → 'internal' (operator watch-topics are shared)
//   - chat.message / chat.status → 'internal' for coordination threads;
//     for intent=talk threads the OWNER's personal space p-<created_by>.
//     Reusing the personal-space id makes "owner only (+admin)" fall out
//     of the one existing visibility mechanism — the owner's
//     VisibleSpaces always contains p-<self>, nobody else's does, and
//     the admin scope is unrestricted — so no second owner-matching
//     predicate is needed (design v2: "シンプルな方を選ぶ").
//
// An event with SpaceID == "" is NEVER delivered to a visibility-
// filtered subscriber (fail-closed: forgetting to stamp a new publish
// site must not become a bypass).
type Event struct {
	Type    string `json:"type"` // e.g. "comment.created"
	Data    any    `json:"data"`
	SpaceID string `json:"-"`
}

func NewEventBus() *EventBus {
	return &EventBus{subs: map[chan Event]func(Event) bool{}}
}

// Subscribe registers a listener; call the returned cancel to leave.
//
// allow is the subscriber's visibility predicate, evaluated per event
// on the publish path (keep it cheap and non-blocking). allow == nil
// subscribes to EVERYTHING — reserved for trusted in-process consumers
// (the webhook dispatcher, which applies its own per-subscription
// space scope); every HTTP-facing subscriber must pass a predicate.
func (b *EventBus) Subscribe(allow func(Event) bool) (<-chan Event, func()) {
	ch := make(chan Event, 16)
	b.mu.Lock()
	b.subs[ch] = allow
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Publish fans out non-blockingly; a full subscriber buffer drops the
// event for that subscriber only (they resync via the recent feed).
func (b *EventBus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch, allow := range b.subs {
		if allow != nil && !allow(e) {
			continue
		}
		select {
		case ch <- e:
		default:
		}
	}
}

// sseVisibility is one SSE connection's view, refreshed periodically so
// a membership revocation reaches long-lived streams (§ slice 4: 5-min
// re-resolution). allow runs on the publisher goroutine — RLock only.
type sseVisibility struct {
	mu           sync.RWMutex
	unrestricted bool
	spaces       map[string]bool
}

func (v *sseVisibility) set(spaces []string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if spaces == nil { // admin contract: nil = unrestricted
		v.unrestricted = true
		v.spaces = nil
		return
	}
	v.unrestricted = false
	v.spaces = make(map[string]bool, len(spaces))
	for _, sp := range spaces {
		v.spaces[sp] = true
	}
}

func (v *sseVisibility) allow(e Event) bool {
	if e.SpaceID == "" {
		// Fail-closed for everyone, admins included: an unstamped event
		// is a bug, and delivering it anywhere would hide that bug.
		return false
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.unrestricted || v.spaces[e.SpaceID]
}

// sseRevisibility is how often a long-lived stream re-resolves the
// subscriber's visible spaces (design v2). Var, not const, so tests can
// shrink it.
var sseRevisibility = 5 * time.Minute

// POST /v1/events/broadcast — publish an EPHEMERAL event to SSE
// listeners (no persistence, no fan-in to storage). This is how an
// external responder daemon shows live progress ("searching …") in the
// frontend while a slow agentic job runs. Only whitelisted types are
// accepted so the stream stays a typed contract, not a free-for-all.
//
// Slice 4: the event is scoped to its thread (chat.status requires
// data.thread_id), and the poster must be the thread's owner, hold the
// admin scope, or be an agent user (the responder runtime posting
// progress into someone else's talk thread — the same exception as
// POST /librarian/chat). Anyone else gets 404, indistinguishable from
// a missing thread (no existence oracle).
func (h *Handler) broadcastEvent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.Type != "chat.status" {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"unsupported event type (allowed: chat.status)", nil)
		return
	}
	threadID, _ := req.Data["thread_id"].(string)
	if threadID == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields,
			"data.thread_id required (chat.status is scoped to its thread)", nil)
		return
	}
	th := h.requireUsableThread(w, r, threadID, true)
	if th == nil {
		return
	}
	h.Events.Publish(Event{Type: req.Type, Data: req.Data, SpaceID: threadEventSpace(th)})
	writeJSON(w, http.StatusAccepted, map[string]any{"published": true})
}

// threadEventSpace maps a chat thread to the space its events are
// delivered under: coordination threads are internal; talk threads
// deliver to the owner's personal space (see the Event doc comment).
func threadEventSpace(th *store.ChatThread) string {
	if th.Intent == "talk" {
		return store.PersonalSpaceID(th.CreatedBy)
	}
	return store.SpaceInternal
}

// GET /v1/events — Server-Sent Events stream of comment.created (and
// future) events for authenticated readers. Heartbeat comments every
// 25s keep proxies (Cloudflare tunnel) from idling the connection out.
// Clients should catch up via GET /v1/comments/recent on (re)connect —
// the stream is a latency optimisation, not the source of truth.
//
// Delivery is visibility-filtered (slice 4): each event carries the
// space it belongs to and only reaches subscribers who can see that
// space at that moment. The view is re-resolved every sseRevisibility
// so revoking a membership takes effect on long-lived streams.
func (h *Handler) streamEvents(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	vis := &sseVisibility{}
	resolve := func() {
		spaces, err := ResolveVisibleSpaces(r.Context(), h.Store, tok)
		if err != nil {
			// Fail-closed: on a resolution error the stream keeps running
			// but delivers nothing until the next successful refresh.
			spaces = []string{}
		}
		vis.set(spaces)
	}
	resolve()

	// ResponseController reaches Flush/deadlines through middleware
	// wrappers (statusRecorder implements Flush+Unwrap). The server's
	// global 30s WriteTimeout would kill a long-lived stream, so lift
	// the write deadline for this connection only.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	_ = rc.SetReadDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	if err := rc.Flush(); err != nil {
		return
	}

	events, cancel := h.Events.Subscribe(vis.allow)
	defer cancel()
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	revis := time.NewTicker(sseRevisibility)
	defer revis.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-revis.C:
			resolve()
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			if err := rc.Flush(); err != nil {
				return
			}
		case e := <-events:
			payload, err := json.Marshal(e.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, payload)
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}
