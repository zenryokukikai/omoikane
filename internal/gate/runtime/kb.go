package runtime

// The omoikane HTTP surface the gate binary talks to, behind the KB
// interface so the whole runtime is testable against httptest and
// swappable once real endpoints can be verified end to end.
//
// Endpoint map (all with the gateway-scoped bearer token):
//
//	ListLibrarians      GET  /v1/gateway/librarians
//	PostAssistantReply  POST /v1/librarian/chat        (author stamped)
//	BroadcastStatus     POST /v1/events/broadcast      (author stamped)
//	ListMessagesSince   GET  /v1/librarian/threads/{id}/messages
//	StreamEvents        GET  /v1/events                (SSE)
//
// TOKEN CONTRACT (G3a adversarial review, MEDIUM): the gateway token
// must be issued USER-LESS — user_id empty, scope "gateway". Binding it
// to a user (an agent-role user especially) would let the stamp path
// mint that user's authority on top of the gateway's. Issue it with the
// admin CLI leaving user_id blank; nothing in this binary works around
// a mis-issued token.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Librarian is one row of GET /v1/gateway/librarians: an ACTIVE
// personal librarian, its owner, and its registered gate instance
// ("" = not yet provisioned on the admin plane — not connectable).
type Librarian struct {
	UserID         string `json:"user_id"`
	AgentID        string `json:"agent_id"`
	Name           string `json:"name"`
	GateInstanceID string `json:"gate_instance_id"`
}

// ChatMessage is the slice of a stored librarian_chat message the
// replay path needs. Extra members in the server response are ignored.
type ChatMessage struct {
	ID           string `json:"id"`
	AuthorRole   string `json:"author_role"`
	AuthorUserID string `json:"author_user_id"`
	Content      string `json:"content"`
}

// StreamEvent is one SSE frame: the event name and its data payload.
type StreamEvent struct {
	Type string
	Data json.RawMessage
}

// RejectedError marks an HTTP outcome where the server DEFINITELY did
// not store the request (4xx). Everything else — 5xx, timeout,
// transport failure — is indeterminate and must be treated as the
// unknown outcome (no fabrication, spec §7).
type RejectedError struct {
	Status int
	Body   string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("kb rejected request: HTTP %d: %s", e.Status, e.Body)
}

// KB is the omoikane API from the gate's seat.
type KB interface {
	// ListLibrarians returns the connection roster.
	ListLibrarians(ctx context.Context) ([]Librarian, error)
	// PostAssistantReply stores one librarian reply into threadID,
	// attributed to authorUserID (the instance's owner), and returns
	// the stored message id. A *RejectedError means definitively not
	// stored; any other error means unknown.
	PostAssistantReply(ctx context.Context, threadID, authorUserID, text string) (messageID string, err error)
	// BroadcastStatus publishes an ephemeral chat.status event for
	// threadID as authorUserID. done=true is the terminal "reply
	// finished" marker; otherwise text is the progress label.
	BroadcastStatus(ctx context.Context, threadID, authorUserID, text string, done bool) error
	// ListMessagesSince returns up to limit messages of threadID newer
	// than sinceID (all from the beginning when sinceID is ""), oldest
	// first.
	ListMessagesSince(ctx context.Context, threadID, sinceID string, limit int) ([]ChatMessage, error)
	// StreamEvents opens the SSE stream and emits events until the
	// stream breaks, then closes the channel. The caller reconnects.
	StreamEvents(ctx context.Context) (<-chan StreamEvent, error)
}

// CursorStore reads and advances the per-thread replay cursor
// (talk_gate_bindings.last_sent_message_id).
type CursorStore interface {
	// Cursor returns the last message id confirmed sent for threadID
	// ("" = replay from the beginning).
	Cursor(ctx context.Context, threadID string) (string, error)
	// Advance records messageID as sent for threadID.
	Advance(ctx context.Context, threadID, messageID string) error
}

// ErrCursorUnavailable — SERVER GAP (follow-up for #104): the store has
// talk_gate_bindings.last_sent_message_id and Set/GetTalkGateBinding,
// but NO authenticated HTTP endpoint exposes them, and inventing an
// unauthenticated one here is out of the question. Until a
// gateway-scoped endpoint exists (e.g. GET/PUT
// /v1/gateway/threads/{id}/cursor), noCursorStore answers this error:
// replay starts from the beginning of the thread and cursor advances
// are dropped. Correctness holds anyway — event origin (= message id)
// is the idempotency key, so re-sent history dedupes core-side — the
// cursor is purely a replay-cost optimization.
//
// Related gap, same follow-up: GET /v1/librarian/threads/{id}/messages
// has no gateway-stamp parameter, and the gateway token is USER-LESS by
// contract (see the token note above), so on a real server the replay
// read itself answers 404 for talk threads until that endpoint (or a
// dedicated gateway replay endpoint) accepts the gateway scope.
var ErrCursorUnavailable = errors.New("gate runtime: no cursor endpoint on the server yet (issue #104 follow-up)")

// noCursorStore is the stand-in until the server grows the endpoint.
type noCursorStore struct{}

func (noCursorStore) Cursor(context.Context, string) (string, error) {
	return "", ErrCursorUnavailable
}

func (noCursorStore) Advance(context.Context, string, string) error {
	return ErrCursorUnavailable
}

// httpKB is the real client.
type httpKB struct {
	base  string
	token string
	hc    *http.Client
	// sseClient has no global timeout (a stream lives arbitrarily
	// long); request cancellation comes from the context.
	sseClient *http.Client
}

func newHTTPKB(baseURL, token string) *httpKB {
	return &httpKB{
		base:      strings.TrimRight(baseURL, "/"),
		token:     token,
		hc:        &http.Client{Timeout: 30 * time.Second},
		sseClient: &http.Client{},
	}
}

func (k *httpKB) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, k.base+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	return req, nil
}

// doJSON runs one request and decodes a 2xx body into out (out nil =
// body discarded). Non-2xx: 4xx becomes *RejectedError (definitely not
// stored), 5xx a plain error (indeterminate).
func (k *httpKB) doJSON(ctx context.Context, method, path string, body, out any) error {
	req, err := k.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := k.hc.Do(req)
	if err != nil {
		return fmt.Errorf("kb: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("kb: %s %s: read response: %w", method, path, err)
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("kb: %s %s: bad success body: %w", method, path, err)
			}
		}
		return nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return &RejectedError{Status: resp.StatusCode, Body: truncateBody(raw, 200)}
	default:
		return fmt.Errorf("kb: %s %s: HTTP %d: %s", method, path, resp.StatusCode, truncateBody(raw, 200))
	}
}

func truncateBody(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

func (k *httpKB) ListLibrarians(ctx context.Context) ([]Librarian, error) {
	var out struct {
		Librarians []Librarian `json:"librarians"`
	}
	if err := k.doJSON(ctx, http.MethodGet, "/v1/gateway/librarians", nil, &out); err != nil {
		return nil, err
	}
	return out.Librarians, nil
}

func (k *httpKB) PostAssistantReply(ctx context.Context, threadID, authorUserID, text string) (string, error) {
	body := map[string]any{
		"thread_id":      threadID,
		"author_role":    "assistant",
		"author_user_id": authorUserID,
		"intent":         "observation",
		"content":        text,
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := k.doJSON(ctx, http.MethodPost, "/v1/librarian/chat", body, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		// Stored-but-unidentifiable: without the id there is no origin
		// to answer, and delivered-with-empty-origin is a wire error.
		// Treat as indeterminate (plain error → unknown outcome).
		return "", errors.New("kb: chat post succeeded without a message id")
	}
	return out.ID, nil
}

func (k *httpKB) BroadcastStatus(ctx context.Context, threadID, authorUserID, text string, done bool) error {
	data := map[string]any{"thread_id": threadID}
	if done {
		data["done"] = true
	} else {
		data["text"] = text
	}
	body := map[string]any{
		"type":           "chat.status",
		"data":           data,
		"author_user_id": authorUserID,
	}
	return k.doJSON(ctx, http.MethodPost, "/v1/events/broadcast", body, nil)
}

func (k *httpKB) ListMessagesSince(ctx context.Context, threadID, sinceID string, limit int) ([]ChatMessage, error) {
	q := url.Values{}
	if sinceID != "" {
		q.Set("since", sinceID)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/v1/librarian/threads/" + url.PathEscape(threadID) + "/messages"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var out struct {
		Messages []ChatMessage `json:"messages"`
	}
	if err := k.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

// StreamEvents opens GET /v1/events and parses the SSE wire minimally:
// "event:" names the type, "data:" lines accumulate the payload, a
// blank line dispatches. Comment lines (": ping") are keep-alives.
func (k *httpKB) StreamEvents(ctx context.Context) (<-chan StreamEvent, error) {
	req, err := k.newRequest(ctx, http.MethodGet, "/v1/events", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := k.sseClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kb: open event stream: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		resp.Body.Close()
		return nil, fmt.Errorf("kb: event stream HTTP %d: %s", resp.StatusCode, string(body))
	}
	ch := make(chan StreamEvent, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		scanSSE(ctx, resp.Body, ch)
	}()
	return ch, nil
}

// scanSSE reads one SSE stream until EOF/error, emitting completed
// events. Split out for direct testing.
func scanSSE(ctx context.Context, r io.Reader, ch chan<- StreamEvent) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var evType string
	var data []byte
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if evType != "" && len(data) > 0 {
				select {
				case ch <- StreamEvent{Type: evType, Data: json.RawMessage(data)}:
				case <-ctx.Done():
					return
				}
			}
			evType, data = "", nil
		case strings.HasPrefix(line, ":"):
			// comment / heartbeat
		case strings.HasPrefix(line, "event:"):
			evType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:"))...)
		}
	}
}
