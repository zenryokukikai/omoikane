// Connection tests: handshake, state machine, dispatch, correlation,
// and the incoming-violation rules, all against a fake core over
// net.Pipe.
package gate

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

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

	want := []string{"id", "m", "protocol", "kind_id", "instance_id", "revision",
		"config_digest", "origin_scope", "address_form", "ingress_discovery",
		"effects", "capabilities"}
	got := keyOrder(t, raw)
	if len(got) != len(want) {
		t.Fatalf("hello keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hello keys = %v, want %v", got, want)
		}
	}
	if s := string(m["effects"]); s != `["say"]` {
		t.Fatalf("effects = %s, want [\"say\"]", s)
	}
	if s := string(m["capabilities"]); s != `["open"]` {
		t.Fatalf("capabilities = %s, want [\"open\"]", s)
	}
	if s := string(m["protocol"]); s != "2" {
		t.Fatalf("protocol = %s, want 2", s)
	}
	if s := fc.str(m, "ingress_discovery"); s != "prebound" {
		t.Fatalf("ingress_discovery = %q", s)
	}

	fc.ok(fc.str(m, "id"), map[string]any{"protocol": 2, "connection_epoch": 41})
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
	fc.send(map[string]any{"id": fc.str(m, "id"),
		"err": map[string]any{"code": "revision_mismatch", "at": nil, "detail": nil}})
	err := <-done
	var we *WireError
	if !errors.As(err, &we) || we.Code != "revision_mismatch" {
		t.Fatalf("NewConn error = %v, want WireError revision_mismatch", err)
	}
}

func TestHappyPath(t *testing.T) {
	var mu sync.Mutex
	var bound []string
	var effects []Effect
	h := &testHandler{
		onBind: func(b Binding) error {
			mu.Lock()
			bound = append(bound, b.BindingID)
			mu.Unlock()
			return nil
		},
		onEffect: func(e Effect) EffectResult {
			mu.Lock()
			effects = append(effects, e)
			mu.Unlock()
			return EffectDelivered("origin-99")
		},
	}
	c, fc := readyConn(t, h)
	if c.Epoch() != 7 {
		t.Fatalf("epoch = %d, want 7", c.Epoch())
	}

	// Two binds in binding_id byte order, each acked before the next.
	for _, id := range []string{testBindingA, testBindingB} {
		fc.send(map[string]any{"id": "core-" + id[len(id)-2:], "m": "bind",
			"binding_id": id, "address": "place-" + id[len(id)-2:]})
		ack, _ := fc.recv()
		if got := fc.str(ack, "id"); got != "core-"+id[len(id)-2:] {
			t.Fatalf("bind ack id = %q", got)
		}
		if string(ack["ok"]) != "{}" {
			t.Fatalf("bind ack ok = %s, want {}", ack["ok"])
		}
	}
	mu.Lock()
	if len(bound) != 2 || bound[0] != testBindingA || bound[1] != testBindingB {
		t.Fatalf("bound = %v", bound)
	}
	mu.Unlock()

	// Event round trip.
	res := sendEventAsync(c, saidEvent(testBindingA, "origin-1"))
	ev, _ := fc.recv()
	if got := fc.str(ev, "m"); got != "event" {
		t.Fatalf("frame m = %q, want event", got)
	}
	if got := fc.str(ev, "kind"); got != "said" {
		t.Fatalf("event kind = %q", got)
	}
	fc.ok(fc.str(ev, "id"), map[string]any{"seq": 42, "binding_id": testBindingA})
	r := <-res
	if r.err != nil || r.seq != 42 || !r.recorded {
		t.Fatalf("SendEvent = (%d, %v, %v), want (42, true, nil)", r.seq, r.recorded, r.err)
	}

	// Effect in, delivered out.
	fc.send(map[string]any{"id": testEffectID, "m": "effect", "binding_id": testBindingA,
		"address": "place-aa", "kind": "say", "payload": map[string]any{"text": "reply"}})
	resp, _ := fc.recv()
	if got := fc.str(resp, "id"); got != testEffectID {
		t.Fatalf("effect response id = %q, want the delivery UUID", got)
	}
	if string(resp["ok"]) != `{"delivered":true,"origin":"origin-99"}` {
		t.Fatalf("effect ok = %s", resp["ok"])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(effects) != 1 || effects[0].ID != testEffectID || effects[0].Kind != "say" {
		t.Fatalf("effects = %+v", effects)
	}
}

func TestSendEventBeforeReadyRefusedLocally(t *testing.T) {
	c, fc := startConn(t) // hello done, ready not sent
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := c.SendEvent(ctx, saidEvent(testBindingA, "origin-1")); !errors.Is(err, ErrNotReady) {
		t.Fatalf("SendEvent error = %v, want ErrNotReady", err)
	}
	// Nothing must have reached the wire: the next read times out.
	fc.c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := fc.br.ReadByte(); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("wire carried bytes before ready (read err = %v)", err)
	}
}

func TestSendEventValidationBlocksWire(t *testing.T) {
	c, fc := readyConn(t, &testHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ev := saidEvent(testBindingA, "") // empty origin
	if _, _, err := c.SendEvent(ctx, ev); err == nil {
		t.Fatal("invalid event accepted")
	}
	fc.c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := fc.br.ReadByte(); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("invalid event reached the wire (read err = %v)", err)
	}
}

func TestBindAcksSerializedInByteOrder(t *testing.T) {
	c, fc := readyConn(t, &testHandler{})
	defer c.Close()
	// The core pipelines both binds (already in binding_id byte order
	// per §6) without waiting; the client must ack serially, one
	// complete response after the other, in that same order.
	fc.sendRaw(`{"id":"b-1","m":"bind","binding_id":"` + testBindingA + `","address":"place-a"}`)
	fc.sendRaw(`{"id":"b-2","m":"bind","binding_id":"` + testBindingB + `","address":"place-b"}`)
	first, _ := fc.recv()
	second, _ := fc.recv()
	if fc.str(first, "id") != "b-1" || fc.str(second, "id") != "b-2" {
		t.Fatalf("ack order = %q, %q; want b-1 then b-2 (binding_id byte order)",
			fc.str(first, "id"), fc.str(second, "id"))
	}
}

func TestEffectDeliveredEmptyOriginClosesWithoutAnswering(t *testing.T) {
	h := &testHandler{onEffect: func(Effect) EffectResult { return EffectDelivered("") }}
	c, fc := readyConn(t, h)
	fc.send(map[string]any{"id": testEffectID, "m": "effect", "binding_id": testBindingA,
		"address": "place-aa", "kind": "say", "payload": map[string]any{}})
	// No response frame: the socket just closes.
	if _, err := fc.br.ReadByte(); err == nil {
		t.Fatal("client answered an empty-origin delivered effect")
	}
	waitClosed(t, c)
}

func TestEffectUnknownOutcomeClosesWithoutAnswering(t *testing.T) {
	h := &testHandler{onEffect: func(Effect) EffectResult { return EffectUnknown() }}
	c, fc := readyConn(t, h)
	fc.send(map[string]any{"id": testEffectID, "m": "effect", "binding_id": testBindingA,
		"address": "place-aa", "kind": "say", "payload": map[string]any{}})
	if _, err := fc.br.ReadByte(); err == nil {
		t.Fatal("client fabricated an answer for an unknown effect outcome")
	}
	waitClosed(t, c)
}

func TestEffectRejectedAndWireErr(t *testing.T) {
	detail := "rate limited"
	results := []EffectResult{EffectRejected(), EffectWireErr("upstream_refused", nil, &detail)}
	i := 0
	h := &testHandler{onEffect: func(Effect) EffectResult {
		r := results[i]
		i++
		return r
	}}
	c, fc := readyConn(t, h)
	defer c.Close()
	fc.send(map[string]any{"id": testEffectID, "m": "effect", "binding_id": testBindingA,
		"address": "place-aa", "kind": "say", "payload": map[string]any{}})
	resp, _ := fc.recv()
	if string(resp["ok"]) != `{"delivered":false}` {
		t.Fatalf("rejected ok = %s", resp["ok"])
	}
	fc.send(map[string]any{"id": testEffectID, "m": "effect", "binding_id": testBindingA,
		"address": "place-aa", "kind": "say", "payload": map[string]any{}})
	resp, _ = fc.recv()
	if string(resp["err"]) != `{"code":"upstream_refused","at":null,"detail":"rate limited"}` {
		t.Fatalf("err = %s", resp["err"])
	}
}

func TestCorrelationInterleavedResponses(t *testing.T) {
	c, fc := readyConn(t, &testHandler{})
	defer c.Close()

	evRes := sendEventAsync(c, saidEvent(testBindingA, "origin-1"))
	evFrame, _ := fc.recv()
	readCh := make(chan struct {
		r   ReadResult
		err error
	}, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r, err := c.ReadEvents(ctx, ReadParams{Address: "place-aa", Limit: 10})
		readCh <- struct {
			r   ReadResult
			err error
		}{r, err}
	}()
	rdFrame, _ := fc.recv()

	// Answer in reverse order of the requests.
	fc.ok(fc.str(rdFrame, "id"), map[string]any{"events": []map[string]any{{
		"seq": 5, "kind": "said", "author": map[string]any{"id": "author-2"},
		"content": map[string]any{"text": "old"}, "origin": "origin-5",
	}}, "next": 6})
	fc.ok(fc.str(evFrame, "id"), map[string]any{"seq": 43, "binding_id": testBindingA})

	rr := <-readCh
	if rr.err != nil || len(rr.r.Events) != 1 || rr.r.Events[0].Seq != 5 ||
		rr.r.Next == nil || *rr.r.Next != 6 {
		t.Fatalf("ReadEvents = %+v, %v", rr.r, rr.err)
	}
	er := <-evRes
	if er.err != nil || er.seq != 43 {
		t.Fatalf("SendEvent = (%d, %v), want (43, nil)", er.seq, er.err)
	}
}

func TestErrResponseSurfacesWireError(t *testing.T) {
	c, fc := readyConn(t, &testHandler{})
	defer c.Close()
	res := sendEventAsync(c, saidEvent(testBindingA, "origin-1"))
	ev, _ := fc.recv()
	fc.send(map[string]any{"id": fc.str(ev, "id"),
		"err": map[string]any{"code": "binding_closed", "at": "binding_id", "detail": nil}})
	r := <-res
	var we *WireError
	if !errors.As(r.err, &we) || we.Code != "binding_closed" || we.At == nil || *we.At != "binding_id" {
		t.Fatalf("SendEvent error = %v, want WireError binding_closed", r.err)
	}
}

func TestDuplicateMemberInIncomingFrameCloses(t *testing.T) {
	c, fc := readyConn(t, &testHandler{})
	res := sendEventAsync(c, saidEvent(testBindingA, "origin-1"))
	ev, _ := fc.recv()
	id := fc.str(ev, "id")
	fc.sendRaw(`{"id":"` + id + `","ok":{"seq":1,"binding_id":"` + testBindingA + `"},"ok":{"seq":2,"binding_id":"` + testBindingA + `"}}`)
	waitClosed(t, c)
	if r := <-res; r.err == nil {
		t.Fatal("SendEvent succeeded over a duplicate-member response")
	}
}

func TestOversizedIncomingFrameCloses(t *testing.T) {
	c, fc := readyConn(t, &testHandler{})
	line := append([]byte(`{"pad":"`), []byte(strings.Repeat("x", MaxFrameBytes))...)
	line = append(line, []byte(`"}`)...)
	// Write may error midway once the client closes; that is the point.
	fc.c.Write(append(line, '\n'))
	waitClosed(t, c)
	if !errors.Is(c.Err(), ErrFrameTooLarge) {
		t.Fatalf("close reason = %v, want ErrFrameTooLarge", c.Err())
	}
}

func TestActivityDeliveredAndViolationDropped(t *testing.T) {
	got := make(chan Activity, 1)
	h := &testHandler{onActivity: func(a Activity) { got <- a }}
	c, fc := readyConn(t, h)
	defer c.Close()

	// Violation first: progress with forbidden kind → dropped, kept.
	fc.send(map[string]any{"m": "activity", "address": "place-aa", "activity_id": "act-1",
		"state": "progress", "kind": "turn", "label": "half"})
	// Valid one next: proves the connection survived and order held.
	fc.send(map[string]any{"m": "activity", "address": "place-aa", "activity_id": "act-2",
		"state": "started", "kind": "turn"})
	select {
	case a := <-got:
		if a.ActivityID != "act-2" || a.State != "started" || a.Kind != "turn" {
			t.Fatalf("activity = %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("valid activity never reached the handler")
	}
	select {
	case a := <-got:
		t.Fatalf("violating activity delivered: %+v", a)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-c.Closed():
		t.Fatal("activity violation closed the connection")
	default:
	}
}

func TestCatchUpSurfacedAndAcked(t *testing.T) {
	got := make(chan CatchUp, 1)
	h := &testHandler{onCatchUp: func(cu CatchUp) error {
		got <- cu
		return nil
	}}
	c, fc := readyConn(t, h)
	defer c.Close()
	fc.send(map[string]any{"id": "cu-1", "m": "catch_up", "binding_id": testBindingA,
		"address": "place-aa",
		"start":   map[string]any{"mode": "cursor", "cursor_b64": "AAECAw==", "cursor_digest": testDigest}})
	ack, _ := fc.recv()
	if fc.str(ack, "id") != "cu-1" || string(ack["ok"]) != "{}" {
		t.Fatalf("catch_up ack = %v", ack)
	}
	cu := <-got
	if cu.Mode != "cursor" || cu.CursorB64 != "AAECAw==" || cu.CursorDigest != testDigest ||
		cu.BindingID != testBindingA || cu.Address != "place-aa" {
		t.Fatalf("catch_up = %+v", cu)
	}
}

func TestUnbindAcked(t *testing.T) {
	var mu sync.Mutex
	var unbound []string
	h := &testHandler{onUnbind: func(b Binding) error {
		mu.Lock()
		unbound = append(unbound, b.BindingID)
		mu.Unlock()
		return nil
	}}
	c, fc := readyConn(t, h)
	defer c.Close()
	fc.send(map[string]any{"id": "u-1", "m": "unbind", "binding_id": testBindingA, "address": "place-aa"})
	ack, _ := fc.recv()
	if fc.str(ack, "id") != "u-1" || string(ack["ok"]) != "{}" {
		t.Fatalf("unbind ack = %v", ack)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(unbound) != 1 || unbound[0] != testBindingA {
		t.Fatalf("unbound = %v", unbound)
	}
}

func TestReadyRequiresHandler(t *testing.T) {
	c, _ := startConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Ready(ctx); err == nil {
		t.Fatal("Ready without handler accepted")
	}
}

func TestSetHandlerAfterReadyRefused(t *testing.T) {
	c, fc := readyConn(t, &testHandler{})
	defer c.Close()
	_ = fc
	if err := c.SetHandler(&testHandler{}); err == nil {
		t.Fatal("SetHandler after Ready accepted")
	}
}

func TestPlaceClosedRoundTrip(t *testing.T) {
	c, fc := readyConn(t, &testHandler{})
	defer c.Close()
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- c.PlaceClosed(ctx, testBindingA, "place-aa", "archived")
	}()
	m, _ := fc.recv()
	if fc.str(m, "m") != "place_closed" || fc.str(m, "reason") != "archived" {
		t.Fatalf("frame = %v", m)
	}
	fc.ok(fc.str(m, "id"), map[string]any{"closed": true})
	if err := <-done; err != nil {
		t.Fatalf("PlaceClosed: %v", err)
	}
	// Bad reason refused locally.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.PlaceClosed(ctx, testBindingA, "place-aa", "bored"); err == nil {
		t.Fatal("unknown reason accepted")
	}
}

func TestSourceCheckpointRoundTrip(t *testing.T) {
	c, fc := readyConn(t, &testHandler{})
	defer c.Close()
	type res struct {
		digest string
		at     int64
		err    error
	}
	done := make(chan res, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d, at, err := c.SourceCheckpoint(ctx, testBindingA, nil, "AAECAw==")
		done <- res{d, at, err}
	}()
	m, raw := fc.recv()
	if fc.str(m, "m") != "source_checkpoint" {
		t.Fatalf("frame = %v", m)
	}
	// expected_cursor_digest must be present as an explicit null, and
	// the frame must not carry an address member (spec §5).
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if v, present := probe["expected_cursor_digest"]; !present || string(v) != "null" {
		t.Fatalf("expected_cursor_digest = %s, present=%v", v, present)
	}
	if _, present := probe["address"]; present {
		t.Fatal("source_checkpoint must not carry address")
	}
	fc.ok(fc.str(m, "id"), map[string]any{"cursor_digest": testDigest, "updated_at": 1234})
	r := <-done
	if r.err != nil || r.digest != testDigest || r.at != 1234 {
		t.Fatalf("SourceCheckpoint = %+v", r)
	}
}

func TestReadParamsValidatedLocally(t *testing.T) {
	c, _ := readyConn(t, &testHandler{})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.ReadEvents(ctx, ReadParams{Address: "place-aa", Limit: 1001}); err == nil {
		t.Fatal("limit 1001 accepted")
	}
	if _, err := c.ReadEvents(ctx, ReadParams{Address: "place-aa", From: -1}); err == nil {
		t.Fatal("negative from accepted")
	}
	if _, err := c.ReadEvents(ctx, ReadParams{Address: ""}); err == nil {
		t.Fatal("empty address accepted")
	}
}

func TestContextCancellationAbandonsRequest(t *testing.T) {
	c, fc := readyConn(t, &testHandler{})
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := c.SendEvent(ctx, saidEvent(testBindingA, "origin-1"))
		done <- err
	}()
	ev, _ := fc.recv()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("SendEvent error = %v, want context.Canceled", err)
	}
	// The late response is now an unknown id: ignored, connection kept.
	fc.ok(fc.str(ev, "id"), map[string]any{"seq": 45, "binding_id": testBindingA})
	res := sendEventAsync(c, saidEvent(testBindingA, "origin-2"))
	ev2, _ := fc.recv()
	fc.ok(fc.str(ev2, "id"), map[string]any{"seq": 46, "binding_id": testBindingA})
	if r := <-res; r.err != nil || r.seq != 46 {
		t.Fatalf("follow-up SendEvent = (%d, %v)", r.seq, r.err)
	}
}
