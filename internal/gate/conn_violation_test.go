// Violation-semantics tests (V3 §3.1, §3.3): framing violations close;
// response-correlation violations (never-issued id, missing m, shape
// not matching the pending kind) close; malformed core requests from
// the trusted peer close; unknown core message m keeps; and the
// dispatch-liveness regression.
package gate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// expectStillServesSay proves the connection stayed usable: a valid say
// must reach the handler and be answered.
func expectStillServesSay(t *testing.T, fc *fakeCore, c *Conn) {
	t.Helper()
	fc.send(map[string]any{"id": testDeliveryID, "m": "say", "binding_id": testBindingA,
		"payload": map[string]any{"text": "still alive?"}})
	resp, _ := fc.recv()
	if got := fc.str(resp, "id"); got != testDeliveryID {
		t.Fatalf("say response id = %q", got)
	}
	if got := fc.str(resp, "m"); got != "err" || fc.str(resp, "code") != "external_rejected" {
		t.Fatalf("say response = %v (default handler rejects)", resp)
	}
	select {
	case <-c.Closed():
		t.Fatal("connection closed")
	default:
	}
}

// TestUnknownCoreMessageDroppedAndKept: an unknown m from the core is
// dropped (effect 0) and the connection kept — V3 has no gateway-side
// unknown_message answer.
func TestUnknownCoreMessageDroppedAndKept(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	fc.sendRaw(`{"id":"t-1","m":"tool"}`)
	expectStillServesSay(t, fc, c)
}

// TestMalformedCoreRequestCloses: a structurally invalid bind/say from
// the trusted core is a contract violation — the connection closes
// (pending deliveries go indeterminate core-side).
func TestMalformedCoreRequestCloses(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"bind missing address", `{"id":"bind:x","m":"bind","binding_id":"` + testBindingA + `"}`},
		{"bind bad binding uuid", `{"id":"b","m":"bind","binding_id":"NOT-A-UUID","address":"a"}`},
		{"bind wrong type", `{"id":"b","m":"bind","binding_id":7,"address":"a"}`},
		{"say missing payload", `{"id":"` + testDeliveryID + `","m":"say","binding_id":"` + testBindingA + `"}`},
		{"say non-uuid delivery id", `{"id":"free-form","m":"say","binding_id":"` + testBindingA + `","payload":{"text":"x"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, fc := runningConn(t, &testHandler{})
			fc.sendRaw(tc.raw)
			waitClosed(t, c)
			if err := c.Err(); err == nil {
				t.Fatal("close reason missing")
			}
		})
	}
}

func TestFrameWithoutMCloses(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	fc.sendRaw(`{"id":"1","seq":5}`)
	waitClosed(t, c)
	if err := c.Err(); err == nil || !strings.Contains(err.Error(), "without m") {
		t.Fatalf("close reason = %v, want frame-without-m", err)
	}
}

func TestNeverIssuedResponseIDCloses(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	fc.ok("no-such-request")
	waitClosed(t, c)
	if err := c.Err(); err == nil || !strings.Contains(err.Error(), "response_invalid") {
		t.Fatalf("close reason = %v, want response_invalid", err)
	}
}

// TestSaidOkWithoutSeqCloses: the said ok shape is {id,m,seq} (§3.3);
// an ok missing the seq member does not match the pending request's
// kind and closes the connection.
func TestSaidOkWithoutSeqCloses(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	res := sendSaidAsync(c, said(testBindingA, "origin-1"))
	ev, _ := fc.recv()
	fc.ok(fc.str(ev, "id")) // plain ok: legal for hello/bind/say, not for said
	waitClosed(t, c)
	if r := <-res; r.err == nil {
		t.Fatal("SendSaid succeeded over a seq-less ok")
	}
	if err := c.Err(); err == nil || !strings.Contains(err.Error(), "response_invalid") {
		t.Fatalf("close reason = %v, want response_invalid", err)
	}
}

func TestSaidOkNonPositiveSeqCloses(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	res := sendSaidAsync(c, said(testBindingA, "origin-1"))
	ev, _ := fc.recv()
	fc.okSeq(fc.str(ev, "id"), 0)
	waitClosed(t, c)
	if r := <-res; r.err == nil {
		t.Fatal("SendSaid succeeded over seq 0")
	}
}

func TestErrResponseWithoutCodeCloses(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	res := sendSaidAsync(c, said(testBindingA, "origin-1"))
	ev, _ := fc.recv()
	fc.send(map[string]any{"id": fc.str(ev, "id"), "m": "err", "detail": nil})
	waitClosed(t, c)
	if r := <-res; r.err == nil {
		t.Fatal("SendSaid succeeded over a code-less err")
	}
}

func TestConsumedResponseIDCloses(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	res := sendSaidAsync(c, said(testBindingA, "origin-1"))
	ev, _ := fc.recv()
	id := fc.str(ev, "id")
	fc.okSeq(id, 1)
	if r := <-res; r.err != nil || r.seq != 1 {
		t.Fatalf("SendSaid = %+v", r)
	}
	fc.okSeq(id, 1) // second response to a consumed id
	waitClosed(t, c)
	if err := c.Err(); err == nil || !strings.Contains(err.Error(), "response_invalid") {
		t.Fatalf("close reason = %v, want response_invalid", err)
	}
}

func TestDuplicateMemberInIncomingFrameCloses(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	res := sendSaidAsync(c, said(testBindingA, "origin-1"))
	ev, _ := fc.recv()
	id := fc.str(ev, "id")
	fc.sendRaw(`{"id":"` + id + `","m":"ok","seq":1,"seq":2}`)
	waitClosed(t, c)
	if r := <-res; r.err == nil {
		t.Fatal("SendSaid succeeded over a duplicate-member response")
	}
}

func TestDuplicateMemberInsidePayloadCloses(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	fc.sendRaw(`{"id":"` + testDeliveryID + `","m":"say","binding_id":"` + testBindingA + `","payload":{"text":"a","text":"b"}}`)
	waitClosed(t, c)
	if !errors.Is(c.Err(), errDuplicateMember) {
		t.Fatalf("close reason = %v, want duplicate member", c.Err())
	}
}

func TestOversizedIncomingFrameCloses(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	line := append([]byte(`{"pad":"`), []byte(strings.Repeat("x", MaxFrameBytes))...)
	line = append(line, []byte(`"}`)...)
	// Write may error midway once the client closes; that is the point.
	fc.c.Write(append(line, '\n'))
	waitClosed(t, c)
	if !errors.Is(c.Err(), ErrFrameTooLarge) {
		t.Fatalf("close reason = %v, want ErrFrameTooLarge", c.Err())
	}
}

func TestInvalidJSONCloses(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	fc.sendRaw(`[1,2,3]`)
	waitClosed(t, c)
	if !errors.Is(c.Err(), errNotObject) {
		t.Fatalf("close reason = %v, want not-an-object", c.Err())
	}
}

// TestDispatchQueueUnboundedKeepsReadLoopLive: regression for the
// backpressure deadlock — OnSay waits on SendSaid while the core
// pipelines far more frames than a bounded queue would hold. The read
// loop must keep draining so the said response still gets through.
func TestDispatchQueueUnboundedKeepsReadLoopLive(t *testing.T) {
	const pipelined = 70
	holder := make(chan *Conn, 1)
	h := &testHandler{onSay: func(Say) SayResult {
		c := <-holder
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, _, err := c.SendSaid(ctx, said(testBindingA, "origin-nested")); err != nil {
			detail := err.Error()
			return SayRejected(&detail)
		}
		return SayDelivered()
	}}
	c, fc := runningConn(t, h)
	holder <- c

	fc.send(map[string]any{"id": testDeliveryID, "m": "say", "binding_id": testBindingA,
		"payload": map[string]any{"text": "x"}})
	for i := 0; i < pipelined; i++ {
		fc.send(map[string]any{"m": "activity", "binding_id": testBindingA,
			"activity_id": "act", "state": "ended"})
	}
	// Only now does the core read: the nested said frame first…
	ev, _ := fc.recv()
	if got := fc.str(ev, "m"); got != "said" {
		t.Fatalf("frame m = %q, want said", got)
	}
	fc.okSeq(fc.str(ev, "id"), 50)
	// …then the say response the handler produced after SendSaid.
	resp, raw := fc.recv()
	if got := fc.str(resp, "id"); got != testDeliveryID {
		t.Fatalf("say response id = %q", got)
	}
	if string(raw) != `{"id":"`+testDeliveryID+`","m":"ok"}` {
		t.Fatalf("say ok = %s", raw)
	}
}
