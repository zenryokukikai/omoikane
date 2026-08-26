// Connection tests: the V3 handshake (hello ok ⇒ RUNNING), said round
// trips, bind/say/activity dispatch, and response correlation — all
// against a fake core over net.Pipe.
package gate

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// TestHelloFrameShape pins the V3 hello to its exact member set (§3.3:
// id, m, protocol, instance_id, revision, config_digest — nothing
// else; kind_id, origin_scope, address_form, ingress_discovery,
// effects, capabilities are gone).
func TestHelloFrameShape(t *testing.T) {
	client, server := net.Pipe()
	fc := newFakeCore(t, server)
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		c, err := NewConn(ctx, client, testHelloParams())
		if c != nil {
			defer c.Close()
		}
		done <- err
	}()
	m, raw := fc.recv()

	sameMembers(t, raw, "id", "m", "protocol", "instance_id", "revision", "config_digest")
	if s := fc.str(m, "m"); s != "hello" {
		t.Fatalf("m = %q, want hello", s)
	}
	if s := string(m["protocol"]); s != "2" {
		t.Fatalf("protocol = %s, want 2", s)
	}
	if s := fc.str(m, "instance_id"); s != testInstanceID {
		t.Fatalf("instance_id = %q", s)
	}
	if s := string(m["revision"]); s != "1" {
		t.Fatalf("revision = %s, want 1", s)
	}
	if s := fc.str(m, "config_digest"); s != testDigest {
		t.Fatalf("config_digest = %q", s)
	}

	fc.ok(fc.str(m, "id"))
	if err := <-done; err != nil {
		t.Fatalf("NewConn: %v", err)
	}
}

func TestHelloErrResponseFailsDial(t *testing.T) {
	client, server := net.Pipe()
	fc := newFakeCore(t, server)
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		_, err := NewConn(ctx, client, testHelloParams())
		done <- err
	}()
	m, _ := fc.recv()
	fc.errResp(fc.str(m, "id"), "revision_mismatch", nil)
	err := <-done
	var we *WireError
	if !errors.As(err, &we) || we.Code != "revision_mismatch" {
		t.Fatalf("NewConn error = %v, want WireError revision_mismatch", err)
	}
}

// TestHappyPath: hello ok ⇒ RUNNING immediately; binds acked with the
// exact {id,m:"ok"} response; a said round-trips with seq; a say
// delivered answers a plain ok with NO origin member (§3.3/§10).
func TestHappyPath(t *testing.T) {
	var mu sync.Mutex
	var bound []string
	var says []Say
	h := &testHandler{
		onBind: func(b Binding) error {
			mu.Lock()
			bound = append(bound, b.BindingID)
			mu.Unlock()
			return nil
		},
		onSay: func(s Say) SayResult {
			mu.Lock()
			says = append(says, s)
			mu.Unlock()
			return SayDelivered()
		},
	}
	c, fc := runningConn(t, h)

	// Two binds, each acked with exactly {id, m:"ok"}.
	for _, id := range []string{testBindingA, testBindingB} {
		fc.send(map[string]any{"id": "bind:" + id, "m": "bind",
			"binding_id": id, "address": "thread-" + id[len(id)-8:]})
		ack, raw := fc.recv()
		if got := fc.str(ack, "id"); got != "bind:"+id {
			t.Fatalf("bind ack id = %q", got)
		}
		sameMembers(t, raw, "id", "m")
		if got := fc.str(ack, "m"); got != "ok" {
			t.Fatalf("bind ack m = %q, want ok", got)
		}
	}
	mu.Lock()
	if len(bound) != 2 || bound[0] != testBindingA || bound[1] != testBindingB {
		t.Fatalf("bound = %v", bound)
	}
	mu.Unlock()

	// Said round trip: exact member set, attachments always present.
	res := sendSaidAsync(c, said(testBindingA, "origin-1"))
	ev, raw := fc.recv()
	sameMembers(t, raw, "id", "m", "binding_id", "origin", "author_id", "text", "attachments")
	if got := fc.str(ev, "m"); got != "said" {
		t.Fatalf("frame m = %q, want said", got)
	}
	if got := string(ev["attachments"]); got != "[]" {
		t.Fatalf("attachments = %s, want []", got)
	}
	fc.okSeq(fc.str(ev, "id"), 42)
	r := <-res
	if r.err != nil || r.seq != 42 || !r.recorded {
		t.Fatalf("SendSaid = (%d, %v, %v), want (42, true, nil)", r.seq, r.recorded, r.err)
	}

	// Say in, delivered out: plain ok, no origin member.
	fc.send(map[string]any{"id": testDeliveryID, "m": "say", "binding_id": testBindingA,
		"payload": map[string]any{"text": "reply"}})
	resp, raw := fc.recv()
	if got := fc.str(resp, "id"); got != testDeliveryID {
		t.Fatalf("say response id = %q, want the delivery UUID", got)
	}
	if string(raw) != `{"id":"`+testDeliveryID+`","m":"ok"}` {
		t.Fatalf("say ok = %s, want exactly {id, m:\"ok\"}", raw)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(says) != 1 || says[0].ID != testDeliveryID || says[0].BindingID != testBindingA {
		t.Fatalf("says = %+v", says)
	}
}

// TestDispatchHeldUntilStart: the core may pipeline binds right behind
// the hello ok; they must dispatch (in order) once Start registers the
// handler, never before.
func TestDispatchHeldUntilStart(t *testing.T) {
	c, fc := startConn(t)
	fc.send(map[string]any{"id": "bind:" + testBindingA, "m": "bind",
		"binding_id": testBindingA, "address": "thread-0000aaaa"})
	// No ack may appear before Start.
	fc.c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := fc.br.ReadByte(); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("bind dispatched before Start (read err = %v)", err)
	}
	fc.c.SetReadDeadline(time.Now().Add(10 * time.Second))

	got := make(chan Binding, 1)
	if err := c.Start(&testHandler{onBind: func(b Binding) error {
		got <- b
		return nil
	}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ack, _ := fc.recv()
	if fc.str(ack, "id") != "bind:"+testBindingA || fc.str(ack, "m") != "ok" {
		t.Fatalf("bind ack = %v", ack)
	}
	b := <-got
	if b.BindingID != testBindingA || b.Address != "thread-0000aaaa" {
		t.Fatalf("binding = %+v", b)
	}
	// Start is single-shot.
	if err := c.Start(&testHandler{}); err == nil {
		t.Fatal("second Start accepted")
	}
}

func TestSendSaidValidationBlocksWire(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for name, s := range map[string]Said{
		"empty origin":      said(testBindingA, ""),
		"bad binding uuid":  {BindingID: "NOT-A-UUID", Origin: "o", AuthorID: "a", Text: "hi"},
		"empty author":      {BindingID: testBindingA, Origin: "o", AuthorID: "", Text: "hi"},
		"no text no attach": {BindingID: testBindingA, Origin: "o", AuthorID: "a"},
		"http attachment":   {BindingID: testBindingA, Origin: "o", AuthorID: "a", Attachments: []Attachment{{Kind: "image", URL: "http://example.invalid/a.png"}}},
		"non-image kind":    {BindingID: testBindingA, Origin: "o", AuthorID: "a", Attachments: []Attachment{{Kind: "video", URL: "https://example.invalid/a.mp4"}}},
	} {
		if _, _, err := c.SendSaid(ctx, s); err == nil {
			t.Fatalf("%s: invalid said accepted", name)
		}
	}
	fc.c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := fc.br.ReadByte(); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("invalid said reached the wire (read err = %v)", err)
	}
}

// TestSaidImageOnly: text may be empty when attachments are nonempty;
// the attachment serializes as {kind, url}.
func TestSaidImageOnly(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	s := Said{
		BindingID:   testBindingA,
		Origin:      "origin-img",
		AuthorID:    "author-1",
		Attachments: []Attachment{{Kind: "image", URL: "https://example.invalid/a.png"}},
	}
	res := sendSaidAsync(c, s)
	ev, _ := fc.recv()
	if got := string(ev["attachments"]); got != `[{"kind":"image","url":"https://example.invalid/a.png"}]` {
		t.Fatalf("attachments = %s", got)
	}
	if got := fc.str(ev, "text"); got != "" {
		t.Fatalf("text = %q, want empty", got)
	}
	fc.okSeq(fc.str(ev, "id"), 7)
	if r := <-res; r.err != nil || r.seq != 7 {
		t.Fatalf("SendSaid = %+v", r)
	}
}

// TestSaidSeqNullMeansDiscarded: seq null means the core did not record
// the said (§3.5) — not a transport error, not a duplicate:
// recorded=false, err nil, connection stays alive.
func TestSaidSeqNullMeansDiscarded(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	res := sendSaidAsync(c, said(testBindingA, "origin-1"))
	ev, _ := fc.recv()
	fc.okSeq(fc.str(ev, "id"), nil)
	r := <-res
	if r.err != nil || r.recorded || r.seq != 0 {
		t.Fatalf("SendSaid = (%d, %v, %v), want (0, false, nil)", r.seq, r.recorded, r.err)
	}
	select {
	case <-c.Closed():
		t.Fatal("discarded said closed the connection")
	default:
	}
	// The connection remains usable for the next said.
	res2 := sendSaidAsync(c, said(testBindingA, "origin-2"))
	ev2, _ := fc.recv()
	fc.okSeq(fc.str(ev2, "id"), 43)
	if r2 := <-res2; r2.err != nil || !r2.recorded || r2.seq != 43 {
		t.Fatalf("follow-up SendSaid = (%d, %v, %v), want (43, true, nil)", r2.seq, r2.recorded, r2.err)
	}
}

// TestSaidDuplicateOriginSameSeq: a re-sent origin is answered with the
// SAME seq as the first delivery (§3.5) — recorded=true both times, and
// seq equality is the caller's idempotency check.
func TestSaidDuplicateOriginSameSeq(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	for attempt := 0; attempt < 2; attempt++ {
		res := sendSaidAsync(c, said(testBindingA, "origin-1"))
		ev, _ := fc.recv()
		fc.okSeq(fc.str(ev, "id"), 42)
		r := <-res
		if r.err != nil || !r.recorded || r.seq != 42 {
			t.Fatalf("SendSaid attempt %d = (%d, %v, %v), want (42, true, nil)",
				attempt, r.seq, r.recorded, r.err)
		}
	}
}

func TestSaidErrResponseSurfacesWireError(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	res := sendSaidAsync(c, said(testBindingA, "origin-1"))
	ev, _ := fc.recv()
	fc.errResp(fc.str(ev, "id"), "binding_closed", "closed by operator")
	r := <-res
	var we *WireError
	if !errors.As(r.err, &we) || we.Code != "binding_closed" ||
		we.Detail == nil || *we.Detail != "closed by operator" {
		t.Fatalf("SendSaid error = %v, want WireError binding_closed", r.err)
	}
	select {
	case <-c.Closed():
		t.Fatal("said err closed the connection (running errs keep)")
	default:
	}
}

// TestSayRejectedAnswersExternalRejected: definite non-acceptance is
// err(code="external_rejected") — the ONLY say failure code (§3.3).
func TestSayRejectedAnswersExternalRejected(t *testing.T) {
	detail := "rate limited"
	h := &testHandler{onSay: func(Say) SayResult { return SayRejected(&detail) }}
	c, fc := runningConn(t, h)
	fc.send(map[string]any{"id": testDeliveryID, "m": "say", "binding_id": testBindingA,
		"payload": map[string]any{"text": "x"}})
	resp, raw := fc.recv()
	sameMembers(t, raw, "id", "m", "code", "detail")
	if fc.str(resp, "m") != "err" || fc.str(resp, "code") != "external_rejected" ||
		fc.str(resp, "detail") != "rate limited" {
		t.Fatalf("say err = %s", raw)
	}
	select {
	case <-c.Closed():
		t.Fatal("rejected say closed the connection")
	default:
	}
}

// TestSayRejectedNilDetailSerializesNull: detail is a required member,
// null when absent (ErrDetail = string|null).
func TestSayRejectedNilDetailSerializesNull(t *testing.T) {
	h := &testHandler{onSay: func(Say) SayResult { return SayRejected(nil) }}
	_, fc := runningConn(t, h)
	fc.send(map[string]any{"id": testDeliveryID, "m": "say", "binding_id": testBindingA,
		"payload": map[string]any{"text": "x"}})
	_, raw := fc.recv()
	want := `{"id":"` + testDeliveryID + `","m":"err","code":"external_rejected","detail":null}`
	if string(raw) != want {
		t.Fatalf("say err = %s, want %s", raw, want)
	}
}

func TestSayUnknownOutcomeClosesWithoutAnswering(t *testing.T) {
	h := &testHandler{onSay: func(Say) SayResult { return SayUnknown() }}
	c, fc := runningConn(t, h)
	fc.send(map[string]any{"id": testDeliveryID, "m": "say", "binding_id": testBindingA,
		"payload": map[string]any{"text": "x"}})
	// No response frame: the socket just closes.
	if _, err := fc.br.ReadByte(); err == nil {
		t.Fatal("client fabricated an answer for an unknown say outcome")
	}
	waitClosed(t, c)
}

func TestBindFailureAnswersBindFailed(t *testing.T) {
	h := &testHandler{onBind: func(Binding) error { return errors.New("no capacity") }}
	_, fc := runningConn(t, h)
	fc.send(map[string]any{"id": "bind:" + testBindingA, "m": "bind",
		"binding_id": testBindingA, "address": "thread-0000aaaa"})
	resp, _ := fc.recv()
	if fc.str(resp, "m") != "err" || fc.str(resp, "code") != "bind_failed" {
		t.Fatalf("bind failure response = %v, want err bind_failed", resp)
	}
}

// TestActivityDispatchedAndViolationDropped: V3 activity carries only
// binding_id/activity_id/state with state started|ended; anything else
// (the old progress state included) is dropped without closing.
func TestActivityDispatchedAndViolationDropped(t *testing.T) {
	got := make(chan Activity, 2)
	h := &testHandler{onActivity: func(a Activity) { got <- a }}
	c, fc := runningConn(t, h)

	// Violations first: unknown state (old "progress"), bad binding id.
	fc.send(map[string]any{"m": "activity", "binding_id": testBindingA,
		"activity_id": "act-0", "state": "progress"})
	fc.send(map[string]any{"m": "activity", "binding_id": "NOT-A-UUID",
		"activity_id": "act-0", "state": "started"})
	// Valid ones next: proves the connection survived and order held.
	fc.send(map[string]any{"m": "activity", "binding_id": testBindingA,
		"activity_id": "act-1", "state": "started"})
	fc.send(map[string]any{"m": "activity", "binding_id": testBindingA,
		"activity_id": "act-1", "state": "ended"})
	for _, want := range []string{"started", "ended"} {
		select {
		case a := <-got:
			if a.ActivityID != "act-1" || a.State != want || a.BindingID != testBindingA {
				t.Fatalf("activity = %+v, want state %s", a, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("valid %s activity never reached the handler", want)
		}
	}
	select {
	case <-c.Closed():
		t.Fatal("activity violation closed the connection")
	default:
	}
}

// TestUnknownMembersIgnored: V3 §3.1 — unrecognized members are ignored
// at every level, in requests and responses alike (the old strict
// unknown-member rejection is gone).
func TestUnknownMembersIgnored(t *testing.T) {
	bound := make(chan Binding, 1)
	h := &testHandler{onBind: func(b Binding) error {
		bound <- b
		return nil
	}}
	c, fc := runningConn(t, h)

	// bind with extra members: still dispatched and acked.
	fc.send(map[string]any{"id": "bind:" + testBindingA, "m": "bind",
		"binding_id": testBindingA, "address": "thread-0000aaaa",
		"future_member": true, "nested": map[string]any{"x": 1}})
	ack, _ := fc.recv()
	if fc.str(ack, "m") != "ok" {
		t.Fatalf("bind with unknown members not acked: %v", ack)
	}
	<-bound

	// said ok with extra members: still resolves.
	res := sendSaidAsync(c, said(testBindingA, "origin-1"))
	ev, _ := fc.recv()
	fc.send(map[string]any{"id": fc.str(ev, "id"), "m": "ok", "seq": 5, "shiny": "new"})
	if r := <-res; r.err != nil || r.seq != 5 {
		t.Fatalf("SendSaid = %+v, want seq 5", r)
	}
}

func TestContextCancellationAbandonsRequest(t *testing.T) {
	c, fc := runningConn(t, &testHandler{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := c.SendSaid(ctx, said(testBindingA, "origin-1"))
		done <- err
	}()
	ev, _ := fc.recv()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("SendSaid error = %v, want context.Canceled", err)
	}
	// The late response is an abandoned id: ignored, connection kept.
	fc.okSeq(fc.str(ev, "id"), 45)
	res := sendSaidAsync(c, said(testBindingA, "origin-2"))
	ev2, _ := fc.recv()
	fc.okSeq(fc.str(ev2, "id"), 46)
	if r := <-res; r.err != nil || r.seq != 46 {
		t.Fatalf("follow-up SendSaid = (%d, %v)", r.seq, r.err)
	}
}
