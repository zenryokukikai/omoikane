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
	return New(f.srv.URL, "owner-123", "https://kb.example.com")
}

// Fresh provision: agent absent, trust row absent, token supplied →
// full pipeline in order, ending with the workspace credential write.
func TestProvisionFresh(t *testing.T) {
	f := newFakeRuntime(t)
	const agent = "plib-u-abc"
	f.on("GET", "/api/agents/"+agent, `null`)
	f.on("POST", "/api/agents", `{"id":"`+agent+`","name":"しおり"}`)
	f.on("PUT", "/api/agents/"+agent, `{"updated":true}`)
	discordState := 0
	f.handlers["GET /api/agents/"+agent+"/discord"] = func(recordedCall) (int, string) {
		if discordState == 0 {
			return 200, `{"configured":false}`
		}
		return 200, `{"configured":true,"enabled":true,"owner_discord_id":"owner-123"}`
	}
	f.handlers["PUT /api/agents/"+agent+"/discord"] = func(recordedCall) (int, string) {
		discordState = 1
		// Mirror the real handler: row written, then the gateway start
		// declines on the empty token and the handler reports ok:false.
		return 200, `{"ok":false,"error":"discord ゲートウェイの起動条件を満たしていません"}`
	}
	f.on("PUT", "/api/agents/"+agent+"/workspace/.kb.curlrc", `{"written":true}`)

	err := f.client().Provision(context.Background(), ProvisionParams{
		AgentID:  agent,
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
		"GET /api/agents/" + agent + "/discord",
		"PUT /api/agents/" + agent + "/discord",
		"GET /api/agents/" + agent + "/discord",
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
		"https://kb.example.com/v1/search", "自動配送", "-K .kb.curlrc"} {
		if !strings.Contains(instr, frag) {
			t.Fatalf("instructions missing %q:\n%s", frag, instr)
		}
	}
	if strings.Contains(instr, "kb_secret_token") {
		t.Fatal("instructions must not embed the plaintext token")
	}
	discordPut := f.calls[4].Body
	if discordPut["owner_discord_id"] != "owner-123" || discordPut["bot_token"] != "" {
		t.Fatalf("discord put payload: %+v", discordPut)
	}
	ws := f.calls[6].Body
	content, _ := ws["content"].(string)
	if !strings.Contains(content, "Authorization: Bearer kb_secret_token") {
		t.Fatalf("curlrc content: %q", content)
	}
}

// Re-save: agent + trust row already exist, no token → update path only
// (PUT agent, PATCH discord), no create, no workspace write.
func TestProvisionResave(t *testing.T) {
	f := newFakeRuntime(t)
	const agent = "plib-u-abc"
	f.on("GET", "/api/agents/"+agent, `{"agent_id":"`+agent+`","name":"旧名"}`)
	f.on("PUT", "/api/agents/"+agent, `{"updated":true}`)
	f.on("GET", "/api/agents/"+agent+"/discord",
		`{"configured":true,"enabled":true,"owner_discord_id":"owner-123"}`)
	f.on("PATCH", "/api/agents/"+agent+"/discord",
		`{"ok":true,"configured":true,"owner_discord_id":"owner-123"}`)

	err := f.client().Provision(context.Background(), ProvisionParams{
		AgentID: agent, UserName: "Kojira", Name: "新名", Persona: "p", KBToken: "",
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	want := []string{
		"GET /api/agents/" + agent,
		"PUT /api/agents/" + agent,
		"GET /api/agents/" + agent + "/discord",
		"PATCH /api/agents/" + agent + "/discord",
	}
	got := f.sequence()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("call sequence: got %v want %v", got, want)
	}
	if f.calls[3].Body["owner_discord_id"] != "owner-123" {
		t.Fatalf("patch payload: %+v", f.calls[3].Body)
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
		AgentID: agent, UserName: "u", Name: "n",
	})
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "agent update") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error should name the step and cause: %v", err)
	}
}

// Trust-row creation that doesn't stick (verify GET still shows no row)
// is an error even though PUT "succeeded".
func TestProvisionTrustRowNotPersisted(t *testing.T) {
	f := newFakeRuntime(t)
	const agent = "plib-u-x"
	f.on("GET", "/api/agents/"+agent, `{"agent_id":"`+agent+`"}`)
	f.on("PUT", "/api/agents/"+agent, `{"updated":true}`)
	f.on("GET", "/api/agents/"+agent+"/discord", `{"configured":false}`)
	f.on("PUT", "/api/agents/"+agent+"/discord", `{"ok":true}`)

	err := f.client().Provision(context.Background(), ProvisionParams{
		AgentID: agent, UserName: "u", Name: "n",
	})
	if err == nil || !strings.Contains(err.Error(), "trust row") {
		t.Fatalf("want trust-row error, got %v", err)
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
		AgentID: agent, UserName: "u", Name: "n",
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("want HTTP 502 error, got %v", err)
	}
}

func TestProvisionValidatesParams(t *testing.T) {
	c := New("http://unused", "o", "http://kb")
	if err := c.Provision(context.Background(), ProvisionParams{}); err == nil {
		t.Fatal("want error for empty params")
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
		return 200, `{"session_id":"s","responses":[]}`
	}
	c := f.client()
	c.talkBackoff = time.Millisecond
	if err := c.DispatchTalk(context.Background(), "plib-u1", "hello"); err != nil {
		t.Fatalf("dispatch after transient failures: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	last := f.calls[len(f.calls)-1]
	if last.Body["user_id"] != "owner-123" || last.Body["content"] != "hello" {
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
		if err := c.DispatchTalk(context.Background(), "a", "x"); err == nil {
			t.Fatalf("%s: want error", name)
		}
		if len(f.calls) != 1 {
			t.Fatalf("%s: attempts = %d, want 1 (no retry — the turn may have run)", name, len(f.calls))
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
	c := New(url, "o", "http://kb")
	c.talkBackoff = 2 * time.Millisecond
	start := time.Now()
	if err := c.DispatchTalk(context.Background(), "a", "x"); err == nil {
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
	c := New(srv.URL, "o", "http://kb")
	c.talkHC.Timeout = 20 * time.Millisecond
	c.talkBackoff = time.Millisecond
	start := time.Now()
	err := c.DispatchTalk(context.Background(), "a", "x")
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
