package runtime

// One personal librarian = one gate instance = one instanceRunner: a
// goroutine that dials the core socket, walks hello → handler → ready,
// serves core traffic until the connection dies, and reconnects with
// exponential backoff. The runner is also the gate.Handler — the
// mapping from the wire to omoikane HTTP lives here:
//
//	OnBind      record binding_id ↔ address (= /talk thread id),
//	            then replay missed human messages from the cursor
//	OnEffect    kind "say" {"text"} → POST /v1/librarian/chat
//	            (author stamped to this instance's owner)
//	OnActivity  started/progress → chat.status {thread_id,text},
//	            ended → chat.status {thread_id,done:true}
//	OnCatchUp   ack no-op (the omoikane-talk kind runs
//	            catch_up_mode=none; replay is omoikane-side via the
//	            binding cursor + origin idempotency)

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

type instanceRunner struct {
	rt     *Runtime
	lib    Librarian
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	conn     *gate.Conn        // live connection, nil between sessions
	bindings map[string]string // address (thread id) → binding_id
}

func (rt *Runtime) newInstanceRunner(parent context.Context, lib Librarian) *instanceRunner {
	ctx, cancel := context.WithCancel(parent)
	return &instanceRunner{
		rt: rt, lib: lib, ctx: ctx, cancel: cancel,
		done:     make(chan struct{}),
		bindings: map[string]string{},
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
// reports whether Ready completed (used to reset the backoff).
func (r *instanceRunner) session() (served bool, err error) {
	rwc, err := r.rt.cfg.Dial(r.ctx, r.rt.cfg.SocketPath)
	if err != nil {
		return false, fmt.Errorf("dial: %w", err)
	}
	conn, err := gate.NewConn(r.ctx, rwc, gate.HelloParams{
		KindID:       opencrab.GateKindID,
		InstanceID:   r.lib.GateInstanceID,
		Revision:     r.rt.cfg.HelloRevision,
		ConfigDigest: opencrab.GateInstanceConfigDigest(),
		OriginScope:  opencrab.GateOriginScope,
		AddressForm:  opencrab.GateThreadAddressForm,
	})
	if err != nil {
		return false, fmt.Errorf("hello: %w", err)
	}
	defer conn.Close()
	if err := conn.SetHandler(r); err != nil {
		return false, err
	}
	// Install the connection BEFORE Ready: the first bind can arrive
	// the moment the core processes the ready ack, and OnBind needs
	// r.conn to kick off the replay. Fresh epoch — the core re-binds
	// everything it wants served, so stale mappings must not leak in.
	r.mu.Lock()
	r.conn = conn
	r.bindings = map[string]string{}
	r.mu.Unlock()
	if err := conn.Ready(r.ctx); err != nil {
		r.mu.Lock()
		r.conn = nil
		r.mu.Unlock()
		return false, fmt.Errorf("ready: %w", err)
	}
	r.rt.log.Info("gate instance connected",
		"instance_id", r.lib.GateInstanceID, "user_id", r.lib.UserID, "epoch", conn.Epoch())

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

// ---- gate.Handler ---------------------------------------------------

// OnBind records the binding locally (the server-side row already
// exists — it was written at thread creation) and kicks off the
// missed-message replay. The replay runs on its own goroutine: the
// dispatch loop is serialized, and a replay doing HTTP + SendEvent
// round-trips must not delay subsequent binds and effects.
func (r *instanceRunner) OnBind(b gate.Binding) error {
	r.mu.Lock()
	r.bindings[b.Address] = b.BindingID
	conn := r.conn
	r.mu.Unlock()
	if conn != nil {
		go r.replay(conn, b)
	}
	return nil
}

func (r *instanceRunner) OnUnbind(b gate.Binding) error {
	r.mu.Lock()
	if r.bindings[b.Address] == b.BindingID {
		delete(r.bindings, b.Address)
	}
	r.mu.Unlock()
	return nil
}

// effectPayload is the say payload. Unknown members are tolerated by
// construction: plain json.Unmarshal into a struct ignores extras.
type effectPayload struct {
	Text string `json:"text"`
}

// OnEffect posts the librarian's reply into the thread. Outcome map
// (spec §7, no fabrication):
//
//	201 + id            → delivered, origin = stored message id
//	malformed payload   → err response (invalid_field), conn kept
//	4xx (not stored)    → rejected
//	5xx/timeout/other   → unknown (socket closes without answering)
func (r *instanceRunner) OnEffect(e gate.Effect) gate.EffectResult {
	var p effectPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		at := "payload"
		detail := "say payload must be a JSON object: " + err.Error()
		return gate.EffectWireErr("invalid_field", &at, &detail)
	}
	if p.Text == "" {
		at := "payload.text"
		detail := "say requires nonempty text"
		return gate.EffectWireErr("invalid_field", &at, &detail)
	}
	ctx, cancel := context.WithTimeout(r.ctx, handlerCallTimeout)
	defer cancel()
	msgID, err := r.rt.kb.PostAssistantReply(ctx, e.Address, r.lib.UserID, p.Text)
	if err == nil {
		return gate.EffectDelivered(msgID)
	}
	var rej *RejectedError
	if errors.As(err, &rej) {
		r.rt.log.Warn("effect rejected by kb",
			"instance_id", r.lib.GateInstanceID, "thread_id", e.Address, "err", err)
		return gate.EffectRejected()
	}
	// Indeterminate: the POST may or may not have stored the message.
	// Answering anything would be fabrication — close without answering
	// (EffectUnknown ⇒ conn closes, core records disconnect).
	r.rt.log.Error("effect outcome unknown; closing connection",
		"instance_id", r.lib.GateInstanceID, "thread_id", e.Address, "err", err)
	return gate.EffectUnknown()
}

// OnActivity translates progress to chat.status broadcasts.
// Best-effort by contract: activity has no response frame, and a lost
// status line only costs UI feedback — errors are logged, never fatal.
// It runs inline on the dispatch loop ON PURPOSE: the "considering…"
// status must reach the UI before the reply that OnEffect posts next.
func (r *instanceRunner) OnActivity(a gate.Activity) {
	ctx, cancel := context.WithTimeout(r.ctx, handlerCallTimeout)
	defer cancel()
	var err error
	switch a.State {
	case "started", "progress":
		err = r.rt.kb.BroadcastStatus(ctx, a.Address, r.lib.UserID, a.Label, false)
	case "ended":
		err = r.rt.kb.BroadcastStatus(ctx, a.Address, r.lib.UserID, "", true)
	}
	if err != nil {
		r.rt.log.Warn("activity status broadcast failed",
			"instance_id", r.lib.GateInstanceID, "thread_id", a.Address,
			"state", a.State, "err", err)
	}
}

// OnCatchUp acks without doing anything: the omoikane-talk kind is
// registered catch_up_mode=none, so the core should never send this.
// If a core does anyway, acking is safe — omoikane replays missed
// inbound itself (OnBind replay + origin idempotency), which gives the
// same at-least-once guarantee the cursor contract would.
func (r *instanceRunner) OnCatchUp(cu gate.CatchUp) error {
	r.rt.log.Warn("unexpected catch_up on a catch_up_mode=none kind; acked as no-op",
		"instance_id", r.lib.GateInstanceID, "binding_id", cu.BindingID, "mode", cu.Mode)
	return nil
}

// ---- replay ---------------------------------------------------------

// replay re-sends human messages the core may have missed while this
// instance was disconnected: list thread messages after the binding
// cursor, SendEvent each with origin = message id (the core dedupes by
// origin, so overlap is harmless), and advance the cursor after each
// confirmed send. Cursor read/advance degrade gracefully — see
// ErrCursorUnavailable in kb.go for the flagged server gap.
func (r *instanceRunner) replay(conn *gate.Conn, b gate.Binding) {
	ctx := r.ctx
	cursor, err := r.rt.cursors.Cursor(ctx, b.Address)
	if err != nil {
		r.rt.logCursorGapOnce(err)
		cursor = "" // replay from the beginning; origin dedup keeps it safe
	}
	for {
		msgs, err := r.rt.kb.ListMessagesSince(ctx, b.Address, cursor, replayPageSize)
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

// sendSaid submits one human message as a said event and advances the
// cursor on success. Reports false when the send failed hard (the
// caller stops; a discarded event — seq null — is a core decision, not
// a failure, and does not stop the replay).
func (r *instanceRunner) sendSaid(ctx context.Context, conn *gate.Conn, bindingID, address, msgID, authorID, text string) bool {
	if authorID == "" {
		// Legacy pre-migration-012 rows lack author_user_id; a talk
		// thread's human speaker is its owner.
		authorID = r.lib.UserID
	}
	sctx, cancel := context.WithTimeout(ctx, handlerCallTimeout)
	defer cancel()
	_, recorded, err := conn.SendEvent(sctx, gate.Event{
		Kind:      "said",
		Address:   address,
		BindingID: bindingID,
		Author:    gate.Author{ID: authorID},
		Content:   gate.Text(text),
		Origin:    msgID,
	})
	if err != nil {
		r.rt.log.Warn("said event failed",
			"instance_id", r.lib.GateInstanceID, "thread_id", address,
			"origin", msgID, "err", err)
		return false
	}
	if !recorded {
		r.rt.log.Info("core discarded said event",
			"instance_id", r.lib.GateInstanceID, "thread_id", address, "origin", msgID)
	}
	if err := r.rt.cursors.Advance(sctx, address, msgID); err != nil {
		r.rt.logCursorGapOnce(err)
	}
	return true
}
