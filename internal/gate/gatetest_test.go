// Test support: a scripted fake core on the far side of a net.Pipe.
// All identifiers in these tests are synthetic (spec rule: examples use
// synthetic identifiers only).
package gate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

const (
	testInstanceID = "0190a1b2-c3d4-7e5f-8a6b-000000000001"
	testDigest     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// bindingA < bindingB in byte order.
	testBindingA = "0190a1b2-c3d4-7e5f-8a6b-0000000000aa"
	testBindingB = "0190a1b2-c3d4-7e5f-8a6b-0000000000bb"
	testEffectID = "0190a1b2-c3d4-7e5f-8a6b-0000000000ee"
)

func testHelloParams() HelloParams {
	return HelloParams{
		KindID:       "test-kind",
		InstanceID:   testInstanceID,
		Revision:     1,
		ConfigDigest: testDigest,
		OriginScope:  "instance",
		AddressForm:  "test-address",
	}
}

// fakeCore drives the core side of a net.Pipe by script.
type fakeCore struct {
	t  *testing.T
	c  net.Conn
	br *bufio.Reader
}

func newFakeCore(t *testing.T, c net.Conn) *fakeCore {
	t.Helper()
	if err := c.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	return &fakeCore{t: t, c: c, br: bufio.NewReader(c)}
}

// recvRaw reads one LF frame (without LF).
func (f *fakeCore) recvRaw() []byte {
	f.t.Helper()
	line, err := f.br.ReadBytes('\n')
	if err != nil {
		f.t.Fatalf("fake core read: %v", err)
	}
	return line[:len(line)-1]
}

// recv reads one frame and returns both the generic map view and the
// raw bytes.
func (f *fakeCore) recv() (map[string]json.RawMessage, []byte) {
	f.t.Helper()
	raw := f.recvRaw()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		f.t.Fatalf("fake core unmarshal %q: %v", raw, err)
	}
	return m, raw
}

// str extracts a string member from a generic frame view.
func (f *fakeCore) str(m map[string]json.RawMessage, key string) string {
	f.t.Helper()
	var s string
	if err := json.Unmarshal(m[key], &s); err != nil {
		f.t.Fatalf("fake core member %s: %v", key, err)
	}
	return s
}

// send writes one value as an LF frame.
func (f *fakeCore) send(v any) {
	f.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		f.t.Fatalf("fake core marshal: %v", err)
	}
	f.sendRaw(string(b))
}

// sendRaw writes s verbatim plus LF.
func (f *fakeCore) sendRaw(s string) {
	f.t.Helper()
	if _, err := f.c.Write(append([]byte(s), '\n')); err != nil {
		f.t.Fatalf("fake core write: %v", err)
	}
}

// ok answers request id with an ok payload.
func (f *fakeCore) ok(id string, payload any) {
	f.send(map[string]any{"id": id, "ok": payload})
}

// startConn runs NewConn against a fresh pipe and serves the hello
// exchange (epoch 7). It returns the connected Conn and the fake core.
func startConn(t *testing.T) (*Conn, *fakeCore) {
	t.Helper()
	client, server := net.Pipe()
	fc := newFakeCore(t, server)
	type res struct {
		c   *Conn
		err error
	}
	ch := make(chan res, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	go func() {
		defer cancel()
		c, err := NewConn(ctx, client, testHelloParams())
		ch <- res{c, err}
	}()
	m, _ := fc.recv()
	if got := fc.str(m, "m"); got != "hello" {
		t.Fatalf("first frame m = %q, want hello", got)
	}
	fc.ok(fc.str(m, "id"), map[string]any{"protocol": 2, "connection_epoch": 7})
	r := <-ch
	if r.err != nil {
		t.Fatalf("NewConn: %v", r.err)
	}
	t.Cleanup(func() { r.c.Close() })
	return r.c, fc
}

// testHandler is a Handler with overridable callbacks; nil callbacks
// succeed silently (bind/unbind/catch_up ack, effect rejected).
type testHandler struct {
	onBind     func(Binding) error
	onUnbind   func(Binding) error
	onEffect   func(Effect) EffectResult
	onActivity func(Activity)
	onCatchUp  func(CatchUp) error
}

func (h *testHandler) OnBind(b Binding) error {
	if h.onBind != nil {
		return h.onBind(b)
	}
	return nil
}

func (h *testHandler) OnUnbind(b Binding) error {
	if h.onUnbind != nil {
		return h.onUnbind(b)
	}
	return nil
}

func (h *testHandler) OnEffect(e Effect) EffectResult {
	if h.onEffect != nil {
		return h.onEffect(e)
	}
	return EffectRejected()
}

func (h *testHandler) OnActivity(a Activity) {
	if h.onActivity != nil {
		h.onActivity(a)
	}
}

func (h *testHandler) OnCatchUp(cu CatchUp) error {
	if h.onCatchUp != nil {
		return h.onCatchUp(cu)
	}
	return nil
}

// readyConn returns a Conn that has completed hello and ready with the
// given handler installed.
func readyConn(t *testing.T, h Handler) (*Conn, *fakeCore) {
	t.Helper()
	c, fc := startConn(t)
	if err := c.SetHandler(h); err != nil {
		t.Fatalf("SetHandler: %v", err)
	}
	errCh := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	go func() {
		defer cancel()
		errCh <- c.Ready(ctx)
	}()
	m, _ := fc.recv()
	if got := fc.str(m, "m"); got != "ready" {
		t.Fatalf("frame m = %q, want ready", got)
	}
	fc.ok(fc.str(m, "id"), map[string]any{})
	if err := <-errCh; err != nil {
		t.Fatalf("Ready: %v", err)
	}
	return c, fc
}

// waitClosed asserts the connection reports closed within the deadline.
func waitClosed(t *testing.T, c *Conn) {
	t.Helper()
	select {
	case <-c.Closed():
	case <-time.After(5 * time.Second):
		t.Fatal("connection did not close")
	}
}

// sendEventAsync runs SendEvent on a goroutine so the test goroutine
// can serve the synchronous pipe.
func sendEventAsync(c *Conn, ev Event) chan struct {
	seq int64
	err error
} {
	ch := make(chan struct {
		seq int64
		err error
	}, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		seq, err := c.SendEvent(ctx, ev)
		ch <- struct {
			seq int64
			err error
		}{seq, err}
	}()
	return ch
}

// saidEvent builds a minimal valid said event on binding b.
func saidEvent(b, origin string) Event {
	return Event{
		Kind:      "said",
		Address:   "place-1",
		BindingID: b,
		Author:    Author{ID: "author-1"},
		Content:   Text("hello world"),
		Origin:    origin,
	}
}

// keyOrder returns the top-level member names of a JSON object in
// document order.
func keyOrder(t *testing.T, data []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		t.Fatalf("keyOrder: not an object: %v", err)
	}
	var keys []string
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			t.Fatalf("keyOrder: %v", err)
		}
		keys = append(keys, kt.(string))
		skipJSONValue(t, dec)
	}
	return keys
}

func skipJSONValue(t *testing.T, dec *json.Decoder) {
	t.Helper()
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("skipJSONValue: %v", err)
	}
	switch tok {
	case json.Delim('{'):
		for dec.More() {
			if _, err := dec.Token(); err != nil { // key
				t.Fatalf("skipJSONValue: %v", err)
			}
			skipJSONValue(t, dec)
		}
		if _, err := dec.Token(); err != nil { // '}'
			t.Fatalf("skipJSONValue: %v", err)
		}
	case json.Delim('['):
		for dec.More() {
			skipJSONValue(t, dec)
		}
		if _, err := dec.Token(); err != nil { // ']'
			t.Fatalf("skipJSONValue: %v", err)
		}
	}
}
