// Violation-table semantics tests (external-gate.md §5): post-ready
// core-frame violations answer err and KEEP the connection, pre-ready
// they close; response-correlation edges (abandoned vs never-issued
// ids, binding_id echo); ready-rollback and dispatch-liveness
// regressions.
package gate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// expectErrFrame reads one frame and asserts it is {id, err:{code}}.
func expectErrFrame(t *testing.T, fc *fakeCore, id, code string) {
	t.Helper()
	m, _ := fc.recv()
	if got := fc.str(m, "id"); got != id {
		t.Fatalf("err frame id = %q, want %q", got, id)
	}
	if m["err"] == nil {
		t.Fatalf("frame %v carries no err member", m)
	}
	var we WireError
	if err := decodeStrictBody(m["err"], &we); err != nil {
		t.Fatalf("err member: %v", err)
	}
	if we.Code != code {
		t.Fatalf("err code = %q, want %q", we.Code, code)
	}
}

// expectEffectStillDispatches proves the connection stayed usable: a
// valid effect must reach the handler and be answered.
func expectEffectStillDispatches(t *testing.T, fc *fakeCore, c *Conn) {
	t.Helper()
	fc.send(map[string]any{"id": testEffectID, "m": "effect", "binding_id": testBindingA,
		"address": "place-aa", "kind": "say", "payload": map[string]any{}})
	resp, _ := fc.recv()
	if got := fc.str(resp, "id"); got != testEffectID {
		t.Fatalf("effect response id = %q", got)
	}
	if string(resp["ok"]) != `{"delivered":false}` {
		t.Fatalf("effect ok = %s", resp["ok"])
	}
	select {
	case <-c.Closed():
		t.Fatal("connection closed")
	default:
	}
}

func TestPostReadyUnknownCoreMessageAnsweredAndKept(t *testing.T) {
	c, fc := readyConn(t, &testHandler{})
	defer c.Close()
	// The violation table names core→gate `tool` explicitly: post-ready
	// it is answered unknown_message (write 0) and the connection kept.
	fc.sendRaw(`{"id":"t-1","m":"tool"}`)
	expectErrFrame(t, fc, "t-1", "unknown_message")
	expectEffectStillDispatches(t, fc, c)
}

func TestPostReadyFieldViolationsAnsweredAndKept(t *testing.T) {
	c, fc := readyConn(t, &testHandler{})
	defer c.Close()
	cases := []struct {
		name, raw, id, code string
	}{
		{"bind unknown member", `{"id":"v-1","m":"bind","binding_id":"` + testBindingA + `","address":"place-a","extra":1}`, "v-1", "unknown_field"},
		{"bind missing address", `{"id":"v-2","m":"bind","binding_id":"` + testBindingA + `"}`, "v-2", "missing_field"},
		{"bind bad binding uuid", `{"id":"v-3","m":"bind","binding_id":"NOT-A-UUID","address":"place-a"}`, "v-3", "invalid_field"},
		{"effect unknown kind", `{"id":"` + testEffectID + `","m":"effect","binding_id":"` + testBindingA + `","address":"place-a","kind":"shout","payload":{}}`, testEffectID, "unknown_enum"},
		{"catch_up unknown mode", `{"id":"v-5","m":"catch_up","binding_id":"` + testBindingA + `","address":"place-a","start":{"mode":"weird"}}`, "v-5", "unknown_enum"},
		{"unbind wrong type", `{"id":"v-6","m":"unbind","binding_id":7,"address":"place-a"}`, "v-6", "invalid_field"},
	}
	for _, tc := range cases {
		fc.sendRaw(tc.raw)
		expectErrFrame(t, fc, tc.id, tc.code)
	}
	expectEffectStillDispatches(t, fc, c)
}

func TestPreReadyCoreViolationCloses(t *testing.T) {
	c, fc := startConn(t) // hello done, ready never sent
	fc.sendRaw(`{"id":"t-1","m":"tool"}`)
	waitClosed(t, c)
	if err := c.Err(); err == nil {
		t.Fatal("close reason missing")
	}
	// No err response may have been written before the close.
	if _, err := fc.br.ReadByte(); err == nil {
		t.Fatal("pre-ready violation was answered instead of closing")
	}
}

func TestNeverIssuedResponseIDCloses(t *testing.T) {
	c, fc := readyConn(t, &testHandler{})
	fc.ok("no-such-request", map[string]any{"seq": 1, "binding_id": testBindingA})
	waitClosed(t, c)
	if err := c.Err(); err == nil || !strings.Contains(err.Error(), "response_invalid") {
		t.Fatalf("close reason = %v, want response_invalid", err)
	}
}

func TestSendEventDuplicateOriginSeqNull(t *testing.T) {
	c, fc := readyConn(t, &testHandler{})
	defer c.Close()
	res := sendEventAsync(c, saidEvent(testBindingA, "origin-1"))
	ev, _ := fc.recv()
	fc.ok(fc.str(ev, "id"), map[string]any{"seq": nil, "binding_id": testBindingA})
	r := <-res
	if r.err != nil || !r.dup || r.seq != 0 {
		t.Fatalf("SendEvent = (%d, %v, %v), want (0, true, nil)", r.seq, r.dup, r.err)
	}
	select {
	case <-c.Closed():
		t.Fatal("duplicate origin closed the connection")
	default:
	}
}

func TestSendEventBindingIDMismatchCloses(t *testing.T) {
	c, fc := readyConn(t, &testHandler{})
	res := sendEventAsync(c, saidEvent(testBindingA, "origin-1"))
	ev, _ := fc.recv()
	fc.ok(fc.str(ev, "id"), map[string]any{"seq": 47, "binding_id": testBindingB})
	r := <-res
	if r.err == nil || !strings.Contains(r.err.Error(), "response_invalid") {
		t.Fatalf("SendEvent error = %v, want response_invalid", r.err)
	}
	waitClosed(t, c)
}

func TestReadyFailureAfterSendClosesConn(t *testing.T) {
	c, fc := startConn(t)
	if err := c.SetHandler(&testHandler{}); err != nil {
		t.Fatalf("SetHandler: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Ready(ctx) }()
	fc.recv() // the ready frame is on the wire now
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Ready error = %v, want context.Canceled", err)
	}
	// The frame was written: no rollback to synchronizing. The conn
	// must be terminally closed, so a second ready frame and a late
	// SetHandler are both impossible.
	waitClosed(t, c)
	if err := c.Ready(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("second Ready error = %v, want ErrClosed", err)
	}
	if err := c.SetHandler(&testHandler{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("SetHandler error = %v, want ErrClosed", err)
	}
}

func TestReadyPreWriteFailureRetryable(t *testing.T) {
	c, fc := startConn(t)
	// Pre-write failure (no handler yet): nothing reached the wire, so
	// the connection stays retryable.
	if err := c.Ready(context.Background()); err == nil {
		t.Fatal("Ready without handler accepted")
	}
	if err := c.SetHandler(&testHandler{}); err != nil {
		t.Fatalf("SetHandler after pre-write Ready failure: %v", err)
	}
	errCh := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { errCh <- c.Ready(ctx) }()
	m, _ := fc.recv()
	if got := fc.str(m, "m"); got != "ready" {
		t.Fatalf("frame m = %q, want ready", got)
	}
	fc.ok(fc.str(m, "id"), map[string]any{})
	if err := <-errCh; err != nil {
		t.Fatalf("retried Ready: %v", err)
	}
}

func TestDispatchQueueUnboundedKeepsReadLoopLive(t *testing.T) {
	// Regression for the backpressure deadlock: OnEffect waits on
	// SendEvent while the core pipelines far more frames than the old
	// bounded queue (64) held. The read loop must keep draining so the
	// event response still gets through.
	const pipelined = 70
	holder := make(chan *Conn, 1)
	h := &testHandler{onEffect: func(Effect) EffectResult {
		c := <-holder
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, _, err := c.SendEvent(ctx, saidEvent(testBindingA, "origin-nested")); err != nil {
			return EffectRejected()
		}
		return EffectDelivered("origin-eff")
	}}
	c, fc := readyConn(t, h)
	defer c.Close()
	holder <- c

	fc.send(map[string]any{"id": testEffectID, "m": "effect", "binding_id": testBindingA,
		"address": "place-aa", "kind": "say", "payload": map[string]any{}})
	for i := 0; i < pipelined; i++ {
		fc.send(map[string]any{"m": "activity", "address": "place-aa",
			"activity_id": "act", "state": "ended"})
	}
	// Only now does the core read: the nested event frame first…
	ev, _ := fc.recv()
	if got := fc.str(ev, "m"); got != "event" {
		t.Fatalf("frame m = %q, want event", got)
	}
	fc.ok(fc.str(ev, "id"), map[string]any{"seq": 50, "binding_id": testBindingA})
	// …then the effect response the handler produced after SendEvent.
	resp, _ := fc.recv()
	if got := fc.str(resp, "id"); got != testEffectID {
		t.Fatalf("effect response id = %q", got)
	}
	if string(resp["ok"]) != `{"delivered":true,"origin":"origin-eff"}` {
		t.Fatalf("effect ok = %s", resp["ok"])
	}
}
