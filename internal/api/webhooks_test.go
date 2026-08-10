package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// Full loop: register a subscription (secret shown once), fire an event
// (directive.created), receive a signed POST, verify the HMAC. Also:
// list omits secrets; a dead receiver never breaks the main flow.
func TestWebhookDelivery(t *testing.T) {
	base, adminTok, _ := testServer(t)

	// Receiver: verifies signature and counts deliveries.
	var got atomic.Int32
	var lastBody []byte
	var lastSig, lastEvent string
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		lastBody = buf.Bytes()
		lastSig = r.Header.Get("X-Omoikane-Signature")
		lastEvent = r.Header.Get("X-Omoikane-Event")
		got.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer recv.Close()

	do := func(method, path string, body any) (int, map[string]any) {
		var br = bytes.NewReader(nil)
		if body != nil {
			b, _ := json.Marshal(body)
			br = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, br)
		req.Header.Set("Authorization", "Bearer "+adminTok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// Register subscription for directive.created.
	code, sub := do("POST", "/v1/admin/webhooks", map[string]any{
		"url": recv.URL, "event_types": []string{"directive.created"}})
	if code != http.StatusCreated {
		t.Fatalf("create webhook: %d %v", code, sub)
	}
	secret, _ := sub["secret"].(string)
	if secret == "" {
		t.Fatalf("secret missing on create")
	}
	// Listing must NOT expose the secret.
	_, lst := do("GET", "/v1/admin/webhooks", nil)
	if b, _ := json.Marshal(lst); bytes.Contains(b, []byte(secret)) {
		t.Fatalf("secret leaked in listing")
	}

	// Fire the event through the real path: create a directive.
	if code, _ := do("POST", "/v1/librarian/directives",
		map[string]string{"role": "scout", "text": "webhook e2e"}); code != http.StatusCreated {
		t.Fatalf("create directive: %d", code)
	}

	deadline := time.After(5 * time.Second)
	for got.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("no delivery within 5s")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
	if lastEvent != "directive.created" {
		t.Fatalf("event header: %q", lastEvent)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(lastBody)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if lastSig != want {
		t.Fatalf("signature mismatch")
	}
	var payload struct {
		Type string `json:"type"`
		Data store.Directive
	}
	if err := json.Unmarshal(lastBody, &payload); err != nil || payload.Data.Text != "webhook e2e" {
		t.Fatalf("payload wrong: %s", lastBody)
	}

	// Dead receiver: deactivate ours, register one to a closed port —
	// creating another directive must still succeed instantly.
	sid, _ := sub["id"].(string)
	if code, _ := do("PATCH", "/v1/admin/webhooks/"+sid, map[string]any{"active": false}); code != 200 {
		t.Fatalf("deactivate: %d", code)
	}
	if code, _ := do("POST", "/v1/admin/webhooks", map[string]any{
		"url": "http://127.0.0.1:1", "event_types": []string{"directive.created"}}); code != http.StatusCreated {
		t.Fatalf("create dead webhook: %d", code)
	}
	start := time.Now()
	if code, _ := do("POST", "/v1/librarian/directives",
		map[string]string{"role": "scout", "text": "dead receiver"}); code != http.StatusCreated {
		t.Fatalf("directive with dead webhook: %d", code)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("main flow blocked by dead webhook")
	}
	// Deactivated sub must not receive the second event.
	before := got.Load()
	time.Sleep(300 * time.Millisecond)
	if got.Load() != before {
		t.Fatalf("deactivated subscription still receiving")
	}
}

// chat.message webhooks carry human speech only: an agent runtime must
// not receive its own (or any agent's) reply back as an event — that
// echo costs the consumer an LLM turn per message just to ignore it
// (#39). SSE consumers are unaffected; this is the webhook pipe's
// contract.
func TestWebhookChatEchoFilter(t *testing.T) {
	base, adminTok, _ := testServer(t)

	var got atomic.Int32
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer recv.Close()

	do := func(method, path string, body any) (int, map[string]any) {
		var br = bytes.NewReader(nil)
		if body != nil {
			b, _ := json.Marshal(body)
			br = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, br)
		req.Header.Set("Authorization", "Bearer "+adminTok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	if code, _ := do("POST", "/v1/admin/webhooks", map[string]any{
		"url": recv.URL, "event_types": []string{"chat.message"}}); code != http.StatusCreated {
		t.Fatalf("create webhook: %d", code)
	}
	_, th := do("POST", "/v1/librarian/threads", map[string]string{
		"title": "echo filter", "intent": "ask-sebastian"})
	tid, _ := th["thread_id"].(string)
	if tid == "" {
		t.Fatalf("no thread id: %v", th)
	}

	// Human speech → delivered.
	if code, _ := do("POST", "/v1/librarian/chat", map[string]string{
		"thread_id": tid, "author_role": "human", "content": "question"}); code != http.StatusCreated {
		t.Fatalf("post human chat")
	}
	deadline := time.After(5 * time.Second)
	for got.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("human chat.message not delivered within 5s")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Agent speech → never delivered.
	if code, _ := do("POST", "/v1/librarian/chat", map[string]string{
		"thread_id": tid, "author_role": "chronicler", "content": "reply"}); code != http.StatusCreated {
		t.Fatalf("post agent chat")
	}
	before := got.Load()
	time.Sleep(400 * time.Millisecond)
	if got.Load() != before {
		t.Fatalf("agent-authored chat.message was delivered (echo not filtered)")
	}
}
