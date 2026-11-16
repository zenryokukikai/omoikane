package runtime

// One personal librarian = one gate instance = one instanceRunner: a
// goroutine that dials the core socket, performs the protocol 3 hello
// (hello ok = RUNNING), serves core traffic until the connection dies,
// and reconnects with exponential backoff. The runner is also the
// gate.Handler — the mapping from the wire to omoikane HTTP lives here.
//
// Protocol 3 session key = the /talk thread id. The core resolves the
// key to its own session (a key it has never seen creates one; threads
// that were bound under protocol 2 keep their conversation, because the
// old binding address was the same thread id).
//
//	on connect  list this instance's gateway-routed threads
//	            (GET /v1/gateway/instances/{id}/threads) and replay
//	            missed human messages of each from its cursor
//	OnSay       payload {"text"} → POST /v1/librarian/chat
//	            (author stamped to this instance's owner); ok is a
//	            plain delivery ack — no message id travels back
//	OnActivity  started → chat.status {thread_id,text:"考え中…"},
//	            ended → chat.status {thread_id,done:true}; read is
//	            not displayed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/zenryokukikai/omoikane/internal/gate"
)

// handlerCallTimeout caps one omoikane HTTP call or core round-trip
// made from a handler callback, so a stalled dependency turns into a
// classified outcome instead of a wedged dispatch loop.
const handlerCallTimeout = 30 * time.Second

// replayPageSize is the page size of the on-connect history replay.
const replayPageSize = 100

// activityStartedStatus is the chat.status text shown while the
// librarian's turn runs. Activity carries no label, so the gateway
// supplies a generic one.
const activityStartedStatus = "考え中…"

type instanceRunner struct {
	rt     *Runtime
	lib    Librarian
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	conn    *gate.Conn      // live connection, nil between sessions
	threads map[string]bool // gateway-routed thread ids (= session keys) known to this runner
}

func (rt *Runtime) newInstanceRunner(parent context.Context, lib Librarian) *instanceRunner {
	ctx, cancel := context.WithCancel(parent)
	return &instanceRunner{
		rt: rt, lib: lib, ctx: ctx, cancel: cancel,
		done:    make(chan struct{}),
		threads: map[string]bool{},
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
		InstanceID: r.lib.GateInstanceID,
		AgentID:    r.lib.AgentID,
	})
	if err != nil {
		return false, fmt.Errorf("hello: %w", err)
	}
	defer conn.Close()
	r.mu.Lock()
	r.conn = conn
	r.mu.Unlock()
	if err := conn.Start(r); err != nil {
		r.mu.Lock()
		r.conn = nil
		r.mu.Unlock()
		return false, fmt.Errorf("start: %w", err)
	}
	r.rt.log.Info("gate instance connected",
		"instance_id", r.lib.GateInstanceID, "user_id", r.lib.UserID)
	// Catch up on human messages sent while this instance was offline.
	// Own goroutine: the replay does HTTP + said round-trips and must
	// not delay the says and activity the core streams meanwhile.
	// Static conformance mode has no KB to replay from.
	if !r.rt.static {
		go r.replayAll(conn)
	}

	select {
	case <-conn.Closed():
	case <-r.ctx.Done():
	}
	r.mu.Lock()
	r.conn = nil
	r.mu.Unlock()
	return true, conn.Err()
}

// liveConn returns the current connection (nil between sessions).
func (r *instanceRunner) liveConn() *gate.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn
}

// isRoutedThread reports whether threadID is a gateway-routed thread of
// this instance. An unknown id triggers one refresh of the routed set
// from omoikane — a thread created after the last refresh is found on
// its first message (thread creation writes the routing row before the
// creating request returns, so it is visible by then).
func (r *instanceRunner) isRoutedThread(ctx context.Context, threadID string) bool {
	r.mu.Lock()
	known := r.threads[threadID]
	r.mu.Unlock()
	if known {
		return true
	}
	if _, err := r.refreshThreads(ctx); err != nil {
		r.rt.log.Warn("gateway thread list refresh failed",
			"instance_id", r.lib.GateInstanceID, "err", err)
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.threads[threadID]
}

// refreshThreads replaces the routed-thread set with omoikane's current
// list for this instance and returns it.
func (r *instanceRunner) refreshThreads(ctx context.Context) ([]GatewayThread, error) {
	cctx, cancel := context.WithTimeout(ctx, handlerCallTimeout)
	defer cancel()
	list, err := r.rt.kb.ListGatewayThreads(cctx, r.lib.GateInstanceID)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(list))
	for _, t := range list {
		set[t.ThreadID] = true
	}
	r.mu.Lock()
	r.threads = set
	r.mu.Unlock()
	return list, nil
}

// ---- gate.Handler ---------------------------------------------------

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
	if r.rt.static {
		// Static conformance mode: there is no KB, so no say can ever
		// be delivered. external_rejected is the honest answer, not a
		// stub — the gateway performs ZERO external I/O here, so the
		// delivery is definitively not accepted (V3 trust principle;
		// same disposition as an unknown binding). This also gives a
		// conformance harness a deterministic err path to exercise.
		detail := "static conformance mode: no KB backend, say is never deliverable (zero external I/O)"
		return gate.SayRejected(&detail)
	}
	var p sayPayload
	if err := json.Unmarshal(s.Payload, &p); err != nil {
		detail := "say payload must be a JSON object: " + err.Error()
		return gate.SayRejected(&detail)
	}
	if p.Text == "" {
		detail := "say payload.text must be a nonempty string"
		return gate.SayRejected(&detail)
	}
	// The session key IS the thread id (the only keys this gateway ever
	// sends). The KB authorizes the post against the thread owner, so a
	// key that is not this owner's thread is rejected there (4xx →
	// external_rejected) — no local map is needed.
	address := s.Session
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

// OnActivity translates started/ended to chat.status broadcasts (read
// and any other state has no display and is dropped).
// Best-effort by contract: activity has no response frame, and a lost
// status line only costs UI feedback — errors are logged, never fatal.
// It runs inline on the dispatch loop ON PURPOSE: the "考え中…" status
// must reach the UI before the reply that OnSay posts next.
func (r *instanceRunner) OnActivity(a gate.Activity) {
	if r.rt.static {
		return // static conformance mode: no KB to broadcast to
	}
	if a.State != "started" && a.State != "ended" {
		return
	}
	address := a.Session
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

// replayAll runs the catch-up for every gateway-routed thread of this
// instance, one thread at a time, starting from each thread's cursor.
func (r *instanceRunner) replayAll(conn *gate.Conn) {
	list, err := r.refreshThreads(r.ctx)
	if err != nil {
		r.rt.log.Warn("replay: listing gateway threads failed; no catch-up this session",
			"instance_id", r.lib.GateInstanceID, "err", err)
		return
	}
	for _, t := range list {
		if r.ctx.Err() != nil {
			return
		}
		if !r.replay(conn, t.ThreadID, t.LastSentMessageID) {
			return // connection trouble; the next session replays again
		}
	}
}

// replay re-sends human messages of one thread the core may have
// missed while this instance was disconnected: list thread messages
// after the cursor, SendSaid each with origin = message id (the core
// answers a re-sent origin with the first seq, so overlap is harmless),
// and advance the cursor after each confirmed send. Reports false when
// the connection failed (the caller stops).
func (r *instanceRunner) replay(conn *gate.Conn, threadID, cursor string) bool {
	ctx := r.ctx
	for {
		// Stamp the read as the thread owner (r.lib.UserID): the
		// user-less gateway token cannot read a talk thread it does not
		// own, and the personal librarian's owner IS that thread's owner.
		msgs, err := r.rt.kb.ListMessagesSince(ctx, threadID, cursor, r.lib.UserID, replayPageSize)
		if err != nil {
			r.rt.log.Warn("replay: listing thread messages failed",
				"instance_id", r.lib.GateInstanceID, "thread_id", threadID, "err", err)
			return true // this thread only; the connection is fine
		}
		if len(msgs) == 0 {
			return true
		}
		for _, m := range msgs {
			cursor = m.ID
			if m.AuthorRole != "human" || m.Content == "" {
				continue
			}
			if !r.sendSaid(ctx, conn, threadID, m.ID, m.AuthorUserID, m.Content) {
				return false
			}
		}
		if len(msgs) < replayPageSize {
			return true
		}
	}
}

// sendSaid submits one human message as a said and advances the cursor
// on success. Reports false when the send failed hard (the caller
// stops; a discarded said — seq null — is a core decision, not a
// failure, and does not stop the replay).
func (r *instanceRunner) sendSaid(ctx context.Context, conn *gate.Conn, address, msgID, authorID, text string) bool {
	if authorID == "" {
		// Legacy pre-migration-012 rows lack author_user_id; a talk
		// thread's human speaker is its owner.
		authorID = r.lib.UserID
	}
	// The gateway is the sole authority on who the speaker is
	// (protocol 3: the core trusts the asserted caller). A personal
	// librarian's /talk thread belongs to its owner, so the owner's
	// own messages are owner turns; anyone else (not reachable today —
	// the KB only lets the owner post) stays an untrusted agent turn.
	caller := gate.CallerAgent
	if authorID == r.lib.UserID {
		caller = gate.CallerOwner
	}
	sctx, cancel := context.WithTimeout(ctx, handlerCallTimeout)
	defer cancel()
	_, recorded, err := conn.SendSaid(sctx, gate.Said{
		Session:  address,
		Origin:   msgID,
		AuthorID: authorID,
		Caller:   caller,
		Text:     text,
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
