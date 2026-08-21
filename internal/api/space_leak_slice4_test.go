package api

// Slice 4 of the space read-ACL (issue #60): event-carrying surfaces.
// The static route matrix lives in space_leak_test.go; this file
// asserts the DELIVERY side — SSE per-subscriber filtering, webhook
// space_scope, the broadcast gate, the agent-user exception on talk
// threads, and the include_chat search scope — event by event.
//
// Delivery-order trick: the EventBus publishes under one mutex and each
// subscriber channel is FIFO, so per-subscriber ordering matches
// publish ordering. Every negative assertion ("outsider must NOT get
// the restricted event") is therefore made non-vacuous by publishing a
// visible SENTINEL event afterwards: if the sentinel arrives first, the
// restricted event was filtered, not merely late.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zenryokukikai/omoikane/internal/store"
)

type sseGot struct {
	Type string
	Raw  string
	Data map[string]any
}

// subscribeSSE opens GET /v1/events with tok and feeds every event into
// the returned channel until ctx is done.
func subscribeSSE(t *testing.T, ctx context.Context, base, tok string) <-chan sseGot {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	out := make(chan sseGot, 16)
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		var typ string
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "event: ") {
				typ = strings.TrimPrefix(line, "event: ")
				continue
			}
			if typ != "" && strings.HasPrefix(line, "data: ") {
				raw := strings.TrimPrefix(line, "data: ")
				var d map[string]any
				_ = json.Unmarshal([]byte(raw), &d)
				out <- sseGot{Type: typ, Raw: raw, Data: d}
				typ = ""
			}
		}
	}()
	// Give the subscription a beat to register before the caller
	// starts publishing (same pattern as events_test.go).
	time.Sleep(200 * time.Millisecond)
	return out
}

func nextSSE(t *testing.T, ch <-chan sseGot, what string) sseGot {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(8 * time.Second):
		t.Fatalf("no SSE event within 8s (waiting for %s)", what)
		return sseGot{}
	}
}

// TestSSECommentSpaceFiltering: comment.created on a restricted entry
// reaches the space member but never a non-member; the internal-space
// comment is the sentinel proving the outsider's stream is alive.
func TestSSECommentSpaceFiltering(t *testing.T) {
	f := newLeakFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	memberCh := subscribeSSE(t, ctx, f.base, f.memberTok)
	outsiderCh := subscribeSSE(t, ctx, f.base, f.outsiderTok)

	if s, raw := doJSON(t, "POST", f.base+"/v1/entries/"+f.secretID+"/comments", f.memberTok,
		map[string]any{"body": leakMarker + " secret side comment"}, nil); s != 201 {
		t.Fatalf("secret comment: %d %s", s, raw)
	}
	if s, raw := doJSON(t, "POST", f.base+"/v1/entries/"+f.internalID+"/comments", f.memberTok,
		map[string]any{"body": "sentinel internal comment"}, nil); s != 201 {
		t.Fatalf("internal comment: %d %s", s, raw)
	}

	// Member: restricted first, then the sentinel (publish order).
	e := nextSSE(t, memberCh, "member restricted comment")
	if e.Type != "comment.created" || !strings.Contains(e.Raw, leakMarker) {
		t.Fatalf("member first event should be the restricted comment: %s %s", e.Type, e.Raw)
	}
	// Outsider: the FIRST event must already be the sentinel.
	e = nextSSE(t, outsiderCh, "outsider sentinel comment")
	if e.Type != "comment.created" || !strings.Contains(e.Raw, "sentinel internal comment") {
		t.Fatalf("outsider first event should be the sentinel: %s %s", e.Type, e.Raw)
	}
	if strings.Contains(e.Raw, leakMarker) || strings.Contains(e.Raw, f.secretID) {
		t.Fatalf("restricted bytes leaked to outsider: %s", e.Raw)
	}
}

// TestSSEChatTalkFiltering: chat.message on a talk thread is delivered
// under the owner's personal space — owner yes, outsider no. The
// coordination-thread message is the outsider's sentinel.
func TestSSEChatTalkFiltering(t *testing.T) {
	f := newLeakFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	ownerCh := subscribeSSE(t, ctx, f.base, f.memberTok)
	outsiderCh := subscribeSSE(t, ctx, f.base, f.outsiderTok)

	if s, raw := doJSON(t, "POST", f.base+"/v1/librarian/chat", f.memberTok,
		map[string]any{"thread_id": f.talkThreadID, "author_role": "human",
			"content": leakMarker + " live talk line"}, nil); s != 201 {
		t.Fatalf("talk post: %d %s", s, raw)
	}
	if s, raw := doJSON(t, "POST", f.base+"/v1/librarian/chat", f.memberTok,
		map[string]any{"thread_id": f.coordThreadID, "author_role": "human",
			"content": "sentinel coordination line"}, nil); s != 201 {
		t.Fatalf("coord post: %d %s", s, raw)
	}

	e := nextSSE(t, ownerCh, "owner talk chat.message")
	if e.Type != "chat.message" || !strings.Contains(e.Raw, leakMarker) {
		t.Fatalf("owner first event should be the talk message: %s %s", e.Type, e.Raw)
	}
	e = nextSSE(t, outsiderCh, "outsider sentinel chat.message")
	if e.Type != "chat.message" || !strings.Contains(e.Raw, "sentinel coordination line") {
		t.Fatalf("outsider first event should be the coordination sentinel: %s %s", e.Type, e.Raw)
	}
	if strings.Contains(e.Raw, leakMarker) {
		t.Fatalf("talk bytes leaked to outsider: %s", e.Raw)
	}
}

// TestBroadcastThreadGate: chat.status into someone else's talk thread
// is 404 for an ordinary user; the owner may post and only the owner
// receives it (coordination-thread status = outsider's sentinel).
func TestBroadcastThreadGate(t *testing.T) {
	f := newLeakFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if s, raw := doJSON(t, "POST", f.base+"/v1/events/broadcast", f.outsiderTok,
		map[string]any{"type": "chat.status",
			"data": map[string]any{"thread_id": f.talkThreadID, "text": "probe"}}, nil); s != 404 {
		t.Fatalf("outsider broadcast into foreign talk thread: %d %s (want 404)", s, raw)
	}

	ownerCh := subscribeSSE(t, ctx, f.base, f.memberTok)
	outsiderCh := subscribeSSE(t, ctx, f.base, f.outsiderTok)

	if s, raw := doJSON(t, "POST", f.base+"/v1/events/broadcast", f.memberTok,
		map[string]any{"type": "chat.status",
			"data": map[string]any{"thread_id": f.talkThreadID, "text": leakMarker + " status"}}, nil); s != 202 {
		t.Fatalf("owner broadcast: %d %s", s, raw)
	}
	if s, raw := doJSON(t, "POST", f.base+"/v1/events/broadcast", f.memberTok,
		map[string]any{"type": "chat.status",
			"data": map[string]any{"thread_id": f.coordThreadID, "text": "sentinel status"}}, nil); s != 202 {
		t.Fatalf("coord broadcast: %d %s", s, raw)
	}

	e := nextSSE(t, ownerCh, "owner chat.status")
	if e.Type != "chat.status" || !strings.Contains(e.Raw, leakMarker) {
		t.Fatalf("owner first event should be the talk status: %s %s", e.Type, e.Raw)
	}
	e = nextSSE(t, outsiderCh, "outsider sentinel chat.status")
	if e.Type != "chat.status" || !strings.Contains(e.Raw, "sentinel status") {
		t.Fatalf("outsider first event should be the sentinel status: %s %s", e.Type, e.Raw)
	}
	if strings.Contains(e.Raw, leakMarker) {
		t.Fatalf("talk status leaked to outsider: %s", e.Raw)
	}
}

// TestSSEVisibilityFailClosed pins the fail-closed core: an event with
// no SpaceID is delivered to NOBODY on the SSE path — not even an
// unrestricted (admin) subscriber — so forgetting to stamp a future
// publish site surfaces as a missing event, never as a leak.
func TestSSEVisibilityFailClosed(t *testing.T) {
	admin := &sseVisibility{}
	admin.set(nil) // nil = unrestricted (admin contract)
	if admin.allow(Event{Type: "x", SpaceID: ""}) {
		t.Fatal("unstamped event delivered to unrestricted subscriber")
	}
	if !admin.allow(Event{Type: "x", SpaceID: "sp-anything"}) {
		t.Fatal("unrestricted subscriber should see every stamped event")
	}

	member := &sseVisibility{}
	member.set([]string{store.SpaceInternal, "p-u-1"})
	if member.allow(Event{Type: "x", SpaceID: ""}) {
		t.Fatal("unstamped event delivered to restricted subscriber")
	}
	if !member.allow(Event{Type: "x", SpaceID: store.SpaceInternal}) {
		t.Fatal("visible space filtered out")
	}
	if member.allow(Event{Type: "x", SpaceID: "sp-hidden"}) {
		t.Fatal("hidden space delivered")
	}

	empty := &sseVisibility{}
	empty.set([]string{}) // fail-closed: sees nothing
	if empty.allow(Event{Type: "x", SpaceID: store.SpaceInternal}) {
		t.Fatal("empty view received an event")
	}
}

// TestSSERevisibility: a long-lived stream re-resolves its view (5 min
// in production, shrunk here) — after the member's group is revoked
// mid-stream, restricted-space events stop arriving. The pre-revocation
// event proves delivery worked; the internal sentinel proves the stream
// is still alive afterwards.
func TestSSERevisibility(t *testing.T) {
	prev := sseRevisibility
	sseRevisibility = 50 * time.Millisecond
	t.Cleanup(func() { sseRevisibility = prev })

	f := newLeakFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	memberCh := subscribeSSE(t, ctx, f.base, f.memberTok)

	// Pre-revocation: the member still receives restricted events.
	if s, raw := doJSON(t, "POST", f.base+"/v1/entries/"+f.secretID+"/comments", f.adminTok,
		map[string]any{"body": leakMarker + " pre-revocation comment"}, nil); s != 201 {
		t.Fatalf("pre-revocation comment: %d %s", s, raw)
	}
	e := nextSSE(t, memberCh, "pre-revocation comment")
	if !strings.Contains(e.Raw, "pre-revocation") {
		t.Fatalf("member should receive the pre-revocation event: %s", e.Raw)
	}

	// Revoke and give the refresh ticker time to fire.
	if err := f.st.RemoveGroupMember(context.Background(), f.groupID, "u-member"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)

	// Restricted event (admin-posted), then the internal sentinel.
	if s, raw := doJSON(t, "POST", f.base+"/v1/entries/"+f.secretID+"/comments", f.adminTok,
		map[string]any{"body": leakMarker + " post-revocation comment"}, nil); s != 201 {
		t.Fatalf("post-revocation comment: %d %s", s, raw)
	}
	if s, raw := doJSON(t, "POST", f.base+"/v1/entries/"+f.internalID+"/comments", f.adminTok,
		map[string]any{"body": "sentinel after revocation"}, nil); s != 201 {
		t.Fatalf("sentinel comment: %d %s", s, raw)
	}
	e = nextSSE(t, memberCh, "post-revocation sentinel")
	if !strings.Contains(e.Raw, "sentinel after revocation") {
		t.Fatalf("revoked member's next event should be the sentinel, got: %s", e.Raw)
	}
	if strings.Contains(e.Raw, "post-revocation") {
		t.Fatalf("restricted event delivered after revocation: %s", e.Raw)
	}
}

// TestWebhookSpaceScope: a space_scope'd subscription receives only
// events from its listed spaces; an unscoped (NULL) subscription keeps
// the deliver-everything contract (trusted infrastructure — the
// existing /talk responder must not break).
func TestWebhookSpaceScope(t *testing.T) {
	f := newLeakFixture(t)

	var scopedN, openN atomic.Int32
	var scopedBodies bytes.Buffer
	scoped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		scopedBodies.Write(buf.Bytes())
		scopedN.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer scoped.Close()
	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openN.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer open.Close()

	if s, raw := doJSON(t, "POST", f.base+"/v1/admin/webhooks", f.adminTok, map[string]any{
		"url": scoped.URL, "event_types": []string{"comment.created"},
		"space_scope": []string{store.SpaceInternal},
	}, nil); s != 201 {
		t.Fatalf("create scoped webhook: %d %s", s, raw)
	}
	if s, raw := doJSON(t, "POST", f.base+"/v1/admin/webhooks", f.adminTok, map[string]any{
		"url": open.URL, "event_types": []string{"comment.created"},
	}, nil); s != 201 {
		t.Fatalf("create open webhook: %d %s", s, raw)
	}
	// An explicit empty space_scope is a footgun (delivers nothing while
	// looking unrestricted) — rejected at creation with guidance.
	if s, raw := doJSON(t, "POST", f.base+"/v1/admin/webhooks", f.adminTok, map[string]any{
		"url": open.URL, "event_types": []string{"comment.created"},
		"space_scope": []string{},
	}, nil); s != 400 || !strings.Contains(string(raw), "omit the field") {
		t.Fatalf("empty space_scope should be 400 with guidance: %d %s", s, raw)
	}

	// Restricted-space event, then internal event.
	if s, raw := doJSON(t, "POST", f.base+"/v1/entries/"+f.secretID+"/comments", f.adminTok,
		map[string]any{"body": leakMarker + " restricted webhook comment"}, nil); s != 201 {
		t.Fatalf("restricted comment: %d %s", s, raw)
	}
	if s, raw := doJSON(t, "POST", f.base+"/v1/entries/"+f.internalID+"/comments", f.adminTok,
		map[string]any{"body": "internal webhook comment"}, nil); s != 201 {
		t.Fatalf("internal comment: %d %s", s, raw)
	}

	// The unscoped subscription must see BOTH deliveries; once it has,
	// dispatch for these events is complete on the shared bus order, so
	// the scoped counter is final modulo in-flight goroutines — poll it
	// briefly to settle.
	deadline := time.After(5 * time.Second)
	for openN.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("unscoped webhook got %d deliveries, want 2", openN.Load())
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
	settle := time.After(500 * time.Millisecond)
	for scopedN.Load() < 1 {
		select {
		case <-settle:
			t.Fatalf("scoped webhook got %d deliveries, want 1 (the internal one)", scopedN.Load())
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
	time.Sleep(300 * time.Millisecond) // let any (buggy) extra delivery land
	if n := scopedN.Load(); n != 1 {
		t.Fatalf("scoped webhook got %d deliveries, want exactly 1", n)
	}
	if b := scopedBodies.String(); strings.Contains(b, leakMarker) || strings.Contains(b, f.secretID) {
		t.Fatalf("restricted bytes reached the internal-scoped webhook: %s", b)
	}
}

// TestTalkAgentException: agent users (users.role=agent) may read and
// write a foreign talk thread — the /talk responder runtime's path
// (reads history, posts the answer, streams chat.status progress).
func TestTalkAgentException(t *testing.T) {
	f := newLeakFixture(t)
	ctx := context.Background()
	if err := f.st.CreateUser(ctx, &store.User{ID: "u-agent", Name: "responder", Role: "agent"}); err != nil {
		t.Fatal(err)
	}
	agentTok, err := f.st.CreateToken(ctx, "u-agent", "agent-tok", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	s, raw := doJSON(t, "GET", f.expand("/v1/librarian/threads/{talkthread}/messages"), agentTok, nil, nil)
	if s != 200 || !strings.Contains(string(raw), leakMarker) {
		t.Fatalf("agent should read the talk history (responder context): %d %s", s, raw)
	}
	if s, raw := doJSON(t, "POST", f.base+"/v1/librarian/chat", agentTok,
		map[string]any{"thread_id": f.talkThreadID, "author_role": "chronicler",
			"content": "agent answer"}, nil); s != 201 {
		t.Fatalf("agent reply into talk thread: %d %s", s, raw)
	}
	if s, raw := doJSON(t, "POST", f.base+"/v1/events/broadcast", agentTok,
		map[string]any{"type": "chat.status",
			"data": map[string]any{"thread_id": f.talkThreadID, "text": "searching"}}, nil); s != 202 {
		t.Fatalf("agent chat.status into talk thread: %d %s", s, raw)
	}

	// The exception covers ONLY the response path: closing someone
	// else's talk thread is not part of it (owner/admin only).
	if s, raw := doJSON(t, "POST", f.expand("/v1/librarian/threads/{talkthread}/close"), agentTok, nil, nil); s != 404 {
		t.Fatalf("agent close of foreign talk thread: %d %s (want 404)", s, raw)
	}

	// The exception is for agent users only: an ordinary human outsider
	// stays locked out (the matrix asserts this too — this is the pair).
	if s, _ := doJSON(t, "GET", f.expand("/v1/librarian/threads/{talkthread}/messages"), f.outsiderTok, nil, nil); s != 404 {
		t.Fatalf("outsider talk messages: %d want 404", s)
	}
}

// TestTalkThread404Indistinguishable: for every thread-addressed route,
// the 404 for a HIDDEN talk thread must be byte-identical to the 404
// for a thread that does not exist — status alone is not enough (the
// third-party review caught differing message strings acting as an
// existence oracle; this pins the whole response body, not just the
// code).
func TestTalkThread404Indistinguishable(t *testing.T) {
	f := newLeakFixture(t)
	const ghost = "thread-00000000" // never minted

	routes := []struct {
		name   string
		method string
		path   func(id string) string // relative
		body   func(id string) any
	}{
		{"messages", "GET",
			func(id string) string { return "/v1/librarian/threads/" + id + "/messages" },
			func(string) any { return nil }},
		{"close", "POST",
			func(id string) string { return "/v1/librarian/threads/" + id + "/close" },
			func(string) any { return nil }},
		{"chat post", "POST",
			func(string) string { return "/v1/librarian/chat" },
			func(id string) any {
				return map[string]any{"thread_id": id, "author_role": "human", "content": "probe"}
			}},
		{"broadcast", "POST",
			func(string) string { return "/v1/events/broadcast" },
			func(id string) any {
				return map[string]any{"type": "chat.status",
					"data": map[string]any{"thread_id": id, "text": "probe"}}
			}},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			hs, hidden := doJSON(t, rt.method, f.base+rt.path(f.talkThreadID), f.outsiderTok,
				rt.body(f.talkThreadID), nil)
			ms, missing := doJSON(t, rt.method, f.base+rt.path(ghost), f.outsiderTok,
				rt.body(ghost), nil)
			if hs != 404 || ms != 404 {
				t.Fatalf("hidden=%d missing=%d, want 404/404 (hidden body=%s missing body=%s)",
					hs, ms, hidden, missing)
			}
			if !bytes.Equal(hidden, missing) {
				t.Fatalf("404 bodies differ (existence oracle):\n hidden: %s\nmissing: %s",
					hidden, missing)
			}
		})
	}
}

// TestChatSearchTalkScope: include_chat searches the caller's OWN talk
// threads plus the shared non-talk chat — and nothing else. (The leak
// matrix asserts the negative for the outsider; here are the pairs.)
func TestChatSearchTalkScope(t *testing.T) {
	f := newLeakFixture(t)

	chatHits := func(tok, query string) string {
		s, raw := doJSON(t, "POST", f.base+"/v1/search", tok,
			map[string]any{"query": query, "include_chat": true}, nil)
		if s != 200 {
			t.Fatalf("search %q: %d %s", query, s, raw)
		}
		var out struct {
			ChatResults []json.RawMessage `json:"chat_results"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(out.ChatResults)
		return string(b)
	}

	// Shared coordination chat: visible to both.
	if hits := chatHits(f.outsiderTok, "coordination shared"); !strings.Contains(hits, "coordination shared message") {
		t.Errorf("outsider should find the coordination message, got %s", hits)
	}
	// Own talk thread: the owner finds their own message.
	if hits := chatHits(f.memberTok, leakMarker); !strings.Contains(hits, leakMarker+" talk message") {
		t.Errorf("owner should find their talk message, got %s", hits)
	}
	// Foreign talk thread: nothing (the matrix also asserts zero marker
	// bytes; this pins the chat_results field specifically).
	if hits := chatHits(f.outsiderTok, leakMarker); strings.Contains(hits, leakMarker) {
		t.Errorf("outsider chat_results leaked talk bytes: %s", hits)
	}
	// Admin (unrestricted contract) sees everything.
	if hits := chatHits(f.adminTok, leakMarker); !strings.Contains(hits, leakMarker+" talk message") {
		t.Errorf("admin should see the talk message, got %s", hits)
	}
}
