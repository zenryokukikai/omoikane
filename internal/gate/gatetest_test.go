// Test support: a scripted fake core on the far side of a net.Pipe.
// All identifiers in these tests are synthetic.
package gate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"sort"
	"testing"
	"time"
)

const (
	testInstanceID = "0190a1b2-c3d4-7e5f-8a6b-000000000001"
	testDigest     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// bindingA < bindingB in byte order.
	testBindingA   = "0190a1b2-c3d4-7e5f-8a6b-0000000000aa"
	testBindingB   = "0190a1b2-c3d4-7e5f-8a6b-0000000000bb"
	testDeliveryID = "0190a1b2-c3d4-7e5f-8a6b-0000000000ee"
)

func testHelloParams() HelloParams {
	return HelloParams{
		InstanceID:   testInstanceID,
		Revision:     1,
		ConfigDigest: testDigest,
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

// ok answers request id with a plain ok (hello/bind/say form).
func (f *fakeCore) ok(id string) {
	f.send(map[string]any{"id": id, "m": "ok"})
}

// okSeq answers a said request with {id, m:"ok", seq}. seq may be nil
// (explicit null: core did not record the said).
func (f *fakeCore) okSeq(id string, seq any) {
	f.send(map[string]any{"id": id, "m": "ok", "seq": seq})
}

// errResp answers request id with {id, m:"err", code, detail}.
func (f *fakeCore) errResp(id, code string, detail any) {
	f.send(map[string]any{"id": id, "m": "err", "code": code, "detail": detail})
}

// startConn runs NewConn against a fresh pipe and serves the hello
// exchange (V3: hello ok ⇒ RUNNING; no ready stage). It returns the
// running Conn and the fake core. Handler dispatch is still held —
// call Start (or use runningConn).
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
	fc.ok(fc.str(m, "id"))
	r := <-ch
	if r.err != nil {
		t.Fatalf("NewConn: %v", r.err)
	}
	t.Cleanup(func() { r.c.Close() })
	return r.c, fc
}

// testHandler is a Handler with overridable callbacks; nil callbacks
// default to bind ack and say rejected.
type testHandler struct {
	onBind     func(Binding) error
	onSay      func(Say) SayResult
	onActivity func(Activity)
}

func (h *testHandler) OnBind(b Binding) error {
	if h.onBind != nil {
		return h.onBind(b)
	}
	return nil
}

func (h *testHandler) OnSay(s Say) SayResult {
	if h.onSay != nil {
		return h.onSay(s)
	}
	return SayRejected(nil)
}

func (h *testHandler) OnActivity(a Activity) {
	if h.onActivity != nil {
		h.onActivity(a)
	}
}

// runningConn returns a Conn that has completed hello with the given
// handler dispatching.
func runningConn(t *testing.T, h Handler) (*Conn, *fakeCore) {
	t.Helper()
	c, fc := startConn(t)
	if err := c.Start(h); err != nil {
		t.Fatalf("Start: %v", err)
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

// saidResult carries one SendSaid outcome across goroutines.
type saidResult struct {
	seq      int64
	recorded bool
	err      error
}

// sendSaidAsync runs SendSaid on a goroutine so the test goroutine can
// serve the synchronous pipe.
func sendSaidAsync(c *Conn, s Said) chan saidResult {
	ch := make(chan saidResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		seq, recorded, err := c.SendSaid(ctx, s)
		ch <- saidResult{seq, recorded, err}
	}()
	return ch
}

// said builds a minimal valid said on binding b.
func said(b, origin string) Said {
	return Said{
		BindingID: b,
		Origin:    origin,
		AuthorID:  "author-1",
		Text:      "hello world",
	}
}

// memberSet returns the sorted top-level member names of a JSON object.
func memberSet(t *testing.T, data []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		t.Fatalf("memberSet: not an object: %v", err)
	}
	var keys []string
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			t.Fatalf("memberSet: %v", err)
		}
		keys = append(keys, kt.(string))
		skipJSONValue(t, dec)
	}
	sort.Strings(keys)
	return keys
}

// sameMembers asserts got carries exactly the wanted member names.
func sameMembers(t *testing.T, raw []byte, want ...string) {
	t.Helper()
	got := memberSet(t, raw)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("members = %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("members = %v, want exactly %v", got, want)
		}
	}
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
