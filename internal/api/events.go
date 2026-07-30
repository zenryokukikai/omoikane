package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// EventBus is a minimal in-process pub/sub for the SSE endpoint. One
// server process (single binary, single instance) means no external
// broker is needed; subscribers that fall behind are dropped rather
// than allowed to block publishers (slow-consumer protection — the
// catch-up path is GET /v1/comments/recent, not an unbounded buffer).
type EventBus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// Event is one server-side occurrence pushed to SSE listeners.
type Event struct {
	Type string `json:"type"` // e.g. "comment.created"
	Data any    `json:"data"`
}

func NewEventBus() *EventBus {
	return &EventBus{subs: map[chan Event]struct{}{}}
}

// Subscribe registers a listener; call the returned cancel to leave.
func (b *EventBus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
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
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// POST /v1/events/broadcast — publish an EPHEMERAL event to SSE
// listeners (no persistence, no fan-in to storage). This is how an
// external responder daemon shows live progress ("searching …") in the
// frontend while a slow agentic job runs. Only whitelisted types are
// accepted so the stream stays a typed contract, not a free-for-all.
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
	h.Events.Publish(Event{Type: req.Type, Data: req.Data})
	writeJSON(w, http.StatusAccepted, map[string]any{"published": true})
}

// GET /v1/events — Server-Sent Events stream of comment.created (and
// future) events for authenticated readers. Heartbeat comments every
// 25s keep proxies (Cloudflare tunnel) from idling the connection out.
// Clients should catch up via GET /v1/comments/recent on (re)connect —
// the stream is a latency optimisation, not the source of truth.
func (h *Handler) streamEvents(w http.ResponseWriter, r *http.Request) {
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

	events, cancel := h.Events.Subscribe()
	defer cancel()
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
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
