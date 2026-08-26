package runtime

// One personal librarian = one gate instance = one instanceRunner: a
// goroutine that dials the core socket, performs the V3 hello (hello ok
// = RUNNING, no ready stage), serves core traffic until the connection
// dies, and reconnects with exponential backoff. The runner is also the
// gate.Handler — the mapping from the wire to omoikane HTTP lives here:
//
//	OnBind      record binding_id ↔ address (= /talk thread id),
//	            then replay missed human messages from the cursor
//	OnSay       payload {"text"} → POST /v1/librarian/chat
//	            (author stamped to this instance's owner); ok is a
//	            plain delivery ack — no message id travels back
//	OnActivity  started → chat.status {thread_id,text:"考え中…"},
//	            ended → chat.status {thread_id,done:true}

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/zenryokukikai/omoikane/internal/gate"
	"github.com/zenryokukikai/omoikane/internal/opencrab"
)

// handlerCallTimeout caps one omoikane HTTP call or core round-trip
// made from a handler callback, so a stalled dependency turns into a
// classified outcome instead of a wedged dispatch loop.
const handlerCallTimeout = 30 * time.Second

// replayPageSize is the page size of the on-bind history replay.
const replayPageSize = 100

// activityStartedStatus is the chat.status text shown while the
// librarian's turn runs. V3 activity carries no label (started/ended
// only), so the gateway supplies a generic one.
const activityStartedStatus = "考え中…"

type instanceRunner struct {
	rt     *Runtime
	lib    Librarian
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	conn      *gate.Conn        // live connection, nil between sessions
	bindings  map[string]string // address (thread id) → binding_id
	addresses map[string]string // binding_id → address (thread id)
}

func (rt *Runtime) newInstanceRunner(parent context.Context, lib Librarian) *instanceRunner {
	ctx, cancel := context.WithCancel(parent)
	return &instanceRunner{
		rt: rt, lib: lib, ctx: ctx, cancel: cancel,
		done:      make(chan struct{}),
		bindings:  map[string]string{},
		addresses: map[string]string{},
	}
}

func (r *instanceRunner) stop() {
	r.cancel()
	<-r.done
}

// run is the per-instance connection loop.
func (r *instanceRunner) run() {
	defer close(r.done)
	backoff := r.rt.cfg.ReconnectMin
	for {
		if r.ctx.Err() != nil {
			return
		}
		served, err := r.session()
		if r.ctx.Err() != nil {
			return
		}
		if served {
			backoff = r.rt.cfg.ReconnectMin // a real session ran; start over
		}
		r.rt.log.Warn("gate connection ended; reconnecting",
			"instance_id", r.lib.GateInstanceID, "user_id", r.lib.UserID,
			"backoff", backoff, "err", err)
		select {
		case <-time.After(backoff):
		case <-r.ctx.Done():
			return
		}
		if backoff *= 2; backoff > r.rt.cfg.ReconnectMax {
			backoff = r.rt.cfg.ReconnectMax
		}
	}
}

// session dials and serves one connection until it closes. served
// reports whether the hello succeeded (used to reset the backoff).
func (r *instanceRunner) session() (served bool, err error) {
	rwc, err := r.rt.cfg.Dial(r.ctx, r.rt.cfg.SocketPath)
	if err != nil {
		return false, fmt.Errorf("dial: %w", err)
	}
	conn, err := gate.NewConn(r.ctx, rwc, gate.HelloParams{
		InstanceID:   r.lib.GateInstanceID,
		Revision:     r.rt.cfg.HelloRevision,
		ConfigDigest: opencrab.GateInstanceConfigDigest(),
	})
	if err != nil {
		return false, fmt.Errorf("hello: %w", err)
	}
	defer conn.Close()
	// Install the connection BEFORE Start: the core sends binds the
	// moment the hello ok is out, and OnBind needs r.conn to kick off
	// the replay (dispatch is held until Start, so no bind runs before
	// this). Fresh maps — the core re-binds everything it wants served,
	// so stale mappings must not leak in.
	r.mu.Lock()
	r.conn = conn
	r.bindings = map[string]string{}
	r.addresses = map[string]string{}
	r.mu.Unlock()
	if err := conn.Start(r); err != nil {
		r.mu.Lock()
		r.conn = nil
		r.mu.Unlock()
		return false, fmt.Errorf("start: %w", err)
	}
	r.rt.log.Info("gate instance connected",
		"instance_id", r.lib.GateInstanceID, "user_id", r.lib.UserID)

	select {
	case <-conn.Closed():
	case <-r.ctx.Done():
	}
	r.mu.Lock()
	r.conn = nil
	r.mu.Unlock()
	return true, conn.Err()
}

// liveBinding returns the current connection and the binding for a
// thread address, or (nil, "") when either is missing.
func (r *instanceRunner) liveBinding(address string) (*gate.Conn, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil {
		return nil, ""
	}
	return r.conn, r.bindings[address]
}

// addressOf resolves a binding_id back to its thread address ("" when
// the binding is unknown on this connection).
func (r *instanceRunner) addressOf(bindingID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.addresses[bindingID]
}

// ---- gate.Handler ---------------------------------------------------

// OnBind records the binding locally (the server-side row already
// exists — it was written at thread creation) and kicks off the
// missed-message replay. The replay runs on its own goroutine: the
// dispatch loop is serialized, and a replay doing HTTP + SendSaid
// round-trips must not delay subsequent binds and says.
func (r *instanceRunner) OnBind(b gate.Binding) error {
	r.mu.Lock()
	r.bindings[b.Address] = b.BindingID
	r.addresses[b.BindingID] = b.Address
	conn := r.conn
	r.mu.Unlock()
	if conn != nil {
		go r.replay(conn, b)
	}
	return nil
}

// sayPayload is the say payload. Unknown members are ignored by
// construction (§3.4): plain json.Unmarshal into a struct drops extras.
type sayPayload struct {
	Text string `json:"text"`
}

// OnSay posts the librarian's reply into the thread. Outcome map (V3
// §3.4, no fabrication):
//
//	201                 → delivered (plain ok; the stored message id
//	                      does not travel back on the wire)
//	text missing/empty  → err external_rejected, zero external I/O
//	4xx (not stored)    → err external_rejected
//	5xx/timeout/other   → unknown (socket closes without answering)
func (r *instanceRunner) OnSay(s gate.Say) gate.SayResult {
	var p sayPayload
	if err := json.Unmarshal(s.Payload, &p); err != nil {
		detail := "say payload must be a JSON object: " + err.Error()
		return gate.SayRejected(&detail)
	}
	if p.Text == "" {
		detail := "say payload.text must be a nonempty string"
		return gate.SayRejected(&detail)
	}
	address := r.addressOf(s.BindingID)
	if address == "" {
		detail := "say binding_id is not bound on this connection"
		return gate.SayRejected(&detail) // zero external I/O: definitely not delivered
	}
	ctx, cancel := context.WithTimeout(r.ctx, handlerCallTimeout)
	defer cancel()
	if _, err := r.rt.kb.PostAssistantReply(ctx, address, r.lib.UserID, p.Text); err != nil {
		var rej *RejectedError
		if errors.As(err, &rej) {
			r.rt.log.Warn("say rejected by kb",
				"instance_id", r.lib.GateInstanceID, "thread_id", address, "err", err)
			detail := fmt.Sprintf("kb rejected the post: HTTP %d", rej.Status)
			return gate.SayRejected(&detail)
		}
		// Indeterminate: the POST may or may not have stored the
		// message. Answering anything would be fabrication — close
		// without answering (SayUnknown ⇒ conn closes, core records the
		// delivery indeterminate).
		r.rt.log.Error("say outcome unknown; closing connection",
			"instance_id", r.lib.GateInstanceID, "thread_id", address, "err", err)
		return gate.SayUnknown()
	}
	return gate.SayDelivered()
}

// OnActivity translates started/ended to chat.status broadcasts.
// Best-effort by contract: activity has no response frame, and a lost
// status line only costs UI feedback — errors are logged, never fatal.
// It runs inline on the dispatch loop ON PURPOSE: the "考え中…" status
// must reach the UI before the reply that OnSay posts next.
func (r *instanceRunner) OnActivity(a gate.Activity) {
	address := r.addressOf(a.BindingID)
	if address == "" {
		return // unknown binding: nothing to display
	}
	ctx, cancel := context.WithTimeout(r.ctx, handlerCallTimeout)
	defer cancel()
	var err error
	switch a.State {
	case "started":
		err = r.rt.kb.BroadcastStatus(ctx, address, r.lib.UserID, activityStartedStatus, false)
	case "ended":
		err = r.rt.kb.BroadcastStatus(ctx, address, r.lib.UserID, "", true)
	}
	if err != nil {
		r.rt.log.Warn("activity status broadcast failed",
			"instance_id", r.lib.GateInstanceID, "thread_id", address,
			"state", a.State, "err", err)
	}
}

// ---- replay ---------------------------------------------------------

// replay re-sends human messages the core may have missed while this
// instance was disconnected: list thread messages after the binding
// cursor, SendSaid each with origin = message id (the core answers a
// re-sent origin with the first seq, so overlap is harmless), and
// advance the cursor after each confirmed send. Cursor read/advance
// degrade gracefully: a cursor error (or the no-op store) just replays
// from the beginning, which origin idempotency makes safe.
func (r *instanceRunner) replay(conn *gate.Conn, b gate.Binding) {
	ctx := r.ctx
	cursor, err := r.rt.cursors.Cursor(ctx, b.Address)
	if err != nil {
		r.rt.logCursorGapOnce(err)
		cursor = "" // replay from the beginning; origin dedup keeps it safe
	}
	for {
		// Stamp the read as the thread owner (r.lib.UserID): the
		// user-less gateway token cannot read a talk thread it does not
		// own, and the personal librarian's owner IS that thread's owner.
		msgs, err := r.rt.kb.ListMessagesSince(ctx, b.Address, cursor, r.lib.UserID, replayPageSize)
		if err != nil {
			r.rt.log.Warn("replay: listing thread messages failed",
				"instance_id", r.lib.GateInstanceID, "thread_id", b.Address, "err", err)
			return
		}
		if len(msgs) == 0 {
			return
		}
		for _, m := range msgs {
			cursor = m.ID
			if m.AuthorRole != "human" || m.Content == "" {
				continue
			}
			if !r.sendSaid(ctx, conn, b.BindingID, b.Address, m.ID, m.AuthorUserID, m.Content) {
				return // connection trouble; the next session replays again
			}
		}
		if len(msgs) < replayPageSize {
			return
		}
	}
}

// sendSaid submits one human message as a said and advances the cursor
// on success. Reports false when the send failed hard (the caller
// stops; a discarded said — seq null — is a core decision, not a
// failure, and does not stop the replay).
func (r *instanceRunner) sendSaid(ctx context.Context, conn *gate.Conn, bindingID, address, msgID, authorID, text string) bool {
	if authorID == "" {
		// Legacy pre-migration-012 rows lack author_user_id; a talk
		// thread's human speaker is its owner.
		authorID = r.lib.UserID
	}
	sctx, cancel := context.WithTimeout(ctx, handlerCallTimeout)
	defer cancel()
	_, recorded, err := conn.SendSaid(sctx, gate.Said{
		BindingID: bindingID,
		Origin:    msgID,
		AuthorID:  authorID,
		Text:      text,
	})
	if err != nil {
		r.rt.log.Warn("said failed",
			"instance_id", r.lib.GateInstanceID, "thread_id", address,
			"origin", msgID, "err", err)
		return false
	}
	if !recorded {
		r.rt.log.Info("core discarded said",
			"instance_id", r.lib.GateInstanceID, "thread_id", address, "origin", msgID)
	}
	if err := r.rt.cursors.Advance(sctx, address, msgID); err != nil {
		r.rt.logCursorGapOnce(err)
	}
	return true
}
