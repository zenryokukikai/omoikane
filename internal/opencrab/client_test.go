package opencrab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// recordedCall is one request the fake runtime saw.
type recordedCall struct {
	Method string
	Path   string
	Body   map[string]any
}

// fakeRuntime is an httptest-backed opencrab stand-in. Handlers are
// keyed by "METHOD path"; unmatched requests fail the test.
type fakeRuntime struct {
	t        *testing.T
	calls    []recordedCall
	handlers map[string]func(call recordedCall) (int, string)
	srv      *httptest.Server
}

func newFakeRuntime(t *testing.T) *fakeRuntime {
	f := &fakeRuntime{t: t, handlers: map[string]func(recordedCall) (int, string){}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		call := recordedCall{Method: r.Method, Path: r.URL.Path}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &call.Body)
		}
		f.calls = append(f.calls, call)
		h, ok := f.handlers[r.Method+" "+r.URL.Path]
		if !ok {
			t.Errorf("unexpected runtime call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			fmt.Fprint(w, `{"error":"unexpected call"}`)
			return
		}
		code, body := h(call)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRuntime) on(method, path string, body string) {
	f.handlers[method+" "+path] = func(recordedCall) (int, string) { return 200, body }
}

func (f *fakeRuntime) sequence() []string {
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.Method + " " + c.Path
	}
	return out
}

func (f *fakeRuntime) client() *Client {
	return New(f.srv.URL, "https://kb.example.com")
}

// Fresh provision: agent absent, token supplied → full pipeline in
// order, ending with the workspace credential write. No trust-row call
// (the runtime has no such table since opencrab D-1097): an unexpected
// /discord call fails the fake runtime.
func TestProvisionFresh(t *testing.T) {
	f := newFakeRuntime(t)
	const agent = "plib-u-abc"
	f.on("GET", "/api/agents/"+agent, `null`)
	f.on("POST", "/api/agents", `{"id":"`+agent+`","name":"しおり"}`)
	f.on("PUT", "/api/agents/"+agent, `{"updated":true}`)
	f.on("PUT", "/api/agents/"+agent+"/workspace/.kb.curlrc", `{"written":true}`)

	err := f.client().Provision(context.Background(), ProvisionParams{
		AgentID:  agent,
		OwnerID:  "u-abc",
		UserName: "Kojira",
		Name:     "しおり",
		Persona:  "丁寧で簡潔。",
		KBToken:  "kb_secret_token",
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	want := []string{
		"GET /api/agents/" + agent,
		"POST /api/agents",
		"PUT /api/agents/" + agent,
		"PUT /api/agents/" + agent + "/workspace/.kb.curlrc",
	}
	got := f.sequence()
	if len(got) != len(want) {
		t.Fatalf("call sequence: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call %d: got %s want %s", i, got[i], want[i])
		}
	}

	// Payload checks.
	create := f.calls[1].Body
	if create["id"] != agent || create["name"] != "しおり" || create["persona_name"] != "しおり" {
		t.Fatalf("create payload: %+v", create)
	}
	put := f.calls[2].Body
	instr, _ := put["instructions"].(string)
	for _, frag := range []string{"しおり", "Kojira", "丁寧で簡潔。",
		"/app/bin/kb-ask", "自動配送"} {
		if !strings.Contains(instr, frag) {
			t.Fatalf("instructions missing %q:\n%s", frag, instr)
		}
	}
	if strings.Contains(instr, "kb_secret_token") {
		t.Fatal("instructions must not embed the plaintext token")
	}
	ws := f.calls[3].Body
	content, _ := ws["content"].(string)
	if !strings.Contains(content, "Authorization: Bearer kb_secret_token") {
		t.Fatalf("curlrc content: %q", content)
	}
}

// Re-save: agent already exists, no token → update path only (PUT
// agent), no create, no workspace write.
func TestProvisionResave(t *testing.T) {
	f := newFakeRuntime(t)
	const agent = "plib-u-abc"
	f.on("GET", "/api/agents/"+agent, `{"agent_id":"`+agent+`","name":"旧名"}`)
	f.on("PUT", "/api/agents/"+agent, `{"updated":true}`)

	err := f.client().Provision(context.Background(), ProvisionParams{
		AgentID: agent, OwnerID: "u-abc", UserName: "Kojira", Name: "新名", Persona: "p", KBToken: "",
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	want := []string{
		"GET /api/agents/" + agent,
		"PUT /api/agents/" + agent,
	}
	got := f.sequence()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("call sequence: got %v want %v", got, want)
	}
}

// A mid-pipeline runtime error surfaces with the failing step named.
func TestProvisionStepError(t *testing.T) {
	f := newFakeRuntime(t)
	const agent = "plib-u-x"
	f.on("GET", "/api/agents/"+agent, `null`)
	f.on("POST", "/api/agents", `{"id":"`+agent+`"}`)
	f.on("PUT", "/api/agents/"+agent, `{"updated":false,"error":"boom"}`)

	err := f.client().Provision(context.Background(), ProvisionParams{
		AgentID: agent, OwnerID: "u-x", UserName: "u", Name: "n",
	})
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "agent update") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error should name the step and cause: %v", err)
	}
}

// HTTP-level failures (non-200) are reported with the status.
func TestProvisionHTTPError(t *testing.T) {
	f := newFakeRuntime(t)
	const agent = "plib-u-x"
	f.handlers["GET /api/agents/"+agent] = func(recordedCall) (int, string) {
		return 502, "bad gateway"
	}
	err := f.client().Provision(context.Background(), ProvisionParams{
		AgentID: agent, OwnerID: "u-x", UserName: "u", Name: "n",
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("want HTTP 502 error, got %v", err)
	}
}

func TestProvisionValidatesParams(t *testing.T) {
	c := New("http://unused", "http://kb")
	if err := c.Provision(context.Background(), ProvisionParams{}); err == nil {
		t.Fatal("want error for empty params")
	}
	// An owner is not optional (issue #137): provisioning without one
	// would write a trust row nobody owns. It must fail before any
	// runtime call — the base URL above is unreachable, so reaching the
	// network at all would hang/error differently.
	err := c.Provision(context.Background(), ProvisionParams{AgentID: "plib-u1", Name: "n"})
	if err == nil || !strings.Contains(err.Error(), "owner id required") {
		t.Fatalf("want owner-id validation error, got %v", err)
	}
}

// DispatchTalk retry contract (issue #73 slice B): transient failures —
// connection errors and 5xx, where the runtime never processed the
// request — are retried (same shape as omoikane's webhook delivery), so
// a runtime restart blip doesn't drop a claimed /talk message.
func TestDispatchTalkRetriesTransient(t *testing.T) {
	f := newFakeRuntime(t)
	const path = "/api/agents/plib-u1/messages"
	attempts := 0
	f.handlers["POST "+path] = func(recordedCall) (int, string) {
		attempts++
		if attempts < 3 {
			return 502, "bad gateway"
		}
		return 200, `{"session_id":"s","responses":[{"agent_id":"plib-u1","content":"承知しました"}]}`
	}
	c := f.client()
	c.talkBackoff = time.Millisecond
	reply, err := c.DispatchTalk(context.Background(), "plib-u1", "u-0f784100", "hello")
	if err != nil {
		t.Fatalf("dispatch after transient failures: %v", err)
	}
	if reply != "承知しました" {
		t.Fatalf("reply = %q, want the runtime's response content (issue #134: the reply is the only channel)", reply)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	last := f.calls[len(f.calls)-1]
	// Caller identity = the librarian's owner, per librarian (#137).
	if last.Body["user_id"] != "u-0f784100" || last.Body["content"] != "hello" {
		t.Fatalf("delivered body wrong: %v", last.Body)
	}
}

// 4xx and error-body responses are FINAL — the runtime processed the
// request and the agent's turn may already have run; a re-send could
// run it twice. Exactly one attempt each.
func TestDispatchTalkNoRetryOnFinal(t *testing.T) {
	cases := map[string]func(recordedCall) (int, string){
		"4xx":        func(recordedCall) (int, string) { return 404, `{"error":"agent not found"}` },
		"error-body": func(recordedCall) (int, string) { return 200, `{"error":"llm failed"}` },
	}
	for name, h := range cases {
		f := newFakeRuntime(t)
		f.handlers["POST /api/agents/a/messages"] = h
		c := f.client()
		c.talkBackoff = time.Millisecond
		if _, err := c.DispatchTalk(context.Background(), "a", "u-1", "x"); err == nil {
			t.Fatalf("%s: want error", name)
		}
		if len(f.calls) != 1 {
			t.Fatalf("%s: attempts = %d, want 1 (no retry — the turn may have run)", name, len(f.calls))
		}
	}
}

// Reply extraction contract (issue #134): the messages endpoint answers
// the agent's turn output as responses[0].content — that text (empty
// and NO_REPLY included, verbatim: suppression is the caller's call) is
// returned to the caller. The one exception: the runtime frames an
// engine failure as HTTP 200 with content "(Error: ...)" — that is an
// error report and must surface as an error, never as reply text.
func TestDispatchTalkReplyExtraction(t *testing.T) {
	cases := []struct {
		name, body, reply string
		wantErr           bool
	}{
		{"reply", `{"session_id":"s","responses":[{"agent_id":"a","content":"答えです"}]}`, "答えです", false},
		{"no-reply-sentinel", `{"session_id":"s","responses":[{"agent_id":"a","content":"NO_REPLY"}]}`, "NO_REPLY", false},
		{"empty-responses", `{"session_id":"s","responses":[]}`, "", false},
		{"engine-error-framing", `{"session_id":"s","responses":[{"agent_id":"a","content":"(Error: llm exploded)"}]}`, "", true},
	}
	for _, tc := range cases {
		f := newFakeRuntime(t)
		body := tc.body
		f.handlers["POST /api/agents/a/messages"] = func(recordedCall) (int, string) { return 200, body }
		reply, err := f.client().DispatchTalk(context.Background(), "a", "u-1", "x")
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: want error, got reply %q", tc.name, reply)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if reply != tc.reply {
			t.Fatalf("%s: reply = %q, want %q", tc.name, reply, tc.reply)
		}
	}
}

// A dead runtime (connection refused) is transient: the retry budget is
// spent before giving up — during a restart the later attempts are the
// ones that land.
func TestDispatchTalkConnectionErrorRetries(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // connections now refused
	c := New(url, "http://kb")
	c.talkBackoff = 2 * time.Millisecond
	start := time.Now()
	if _, err := c.DispatchTalk(context.Background(), "a", "u-1", "x"); err == nil {
		t.Fatal("want error against a dead runtime")
	}
	// Both backoff sleeps (2ms + 4ms) must have run — proof the
	// transport error was classified transient and retried.
	if time.Since(start) < 6*time.Millisecond {
		t.Fatal("retries skipped for a connection error")
	}
}

// A timeout while awaiting the response is FINAL (issue #79): the
// messages endpoint is synchronous over the whole agent turn, so a slow
// turn outlives the client timeout while the request HAS reached the
// runtime — a re-send queues a second turn and duplicates the reply
// (three identical replies in prod). Exactly one attempt.
func TestDispatchTalkNoRetryOnResponseTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hold the response until the client has timed out
	}))
	defer srv.Close()
	defer close(release)
	c := New(srv.URL, "http://kb")
	c.talkHC.Timeout = 20 * time.Millisecond
	c.talkBackoff = time.Millisecond
	start := time.Now()
	_, err := c.DispatchTalk(context.Background(), "a", "u-1", "x")
	if err == nil {
		t.Fatal("want error on response timeout")
	}
	// One attempt only: total time ~ one client timeout, nowhere near
	// three timeouts plus backoffs.
	if elapsed := time.Since(start); elapsed > 45*time.Millisecond {
		t.Fatalf("took %v — looks like the timeout was retried", elapsed)
	}
	var te *transientError
	if errors.As(err, &te) {
		t.Fatal("response timeout classified transient — re-send can double-run the turn")
	}
}
