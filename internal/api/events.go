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

// GET /v1/events — Server-Sent Events stream of comment.created (and
// future) events for authenticated readers. Heartbeat comments every
// 25s keep proxies (Cloudflare tunnel) from idling the connection out.
// Clients should catch up via GET /v1/comments/recent on (re)connect —
// the stream is a latency optimisation, not the source of truth.
func (h *Handler) streamEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, CodeInternal, "streaming unsupported", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()

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
			fl.Flush()
		case e := <-events:
			payload, err := json.Marshal(e.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, payload)
			fl.Flush()
		}
	}
}
