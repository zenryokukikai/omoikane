// Package opencrab is omoikane's provisioning client for the opencrab
// agent runtime (issue #73). The runtime has no auth and lives on the
// internal network only — omoikane is the authenticated wrapper in
// front of it, so the runtime URL and its API shapes never reach the
// browser. This package owns the "settings saved → agent exists on the
// runtime" translation (slice A) and the per-message dispatch of /talk
// traffic to a provisioned agent (DispatchTalk, slice B).
//
// API shapes mirror opencrab crates/server/src/api/{agents,workspace}.rs:
// handlers answer HTTP 200 with an {"error": "..."} JSON body on
// failure, so every call checks the body, not just the status code.
package opencrab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Client provisions personal librarian agents onto one opencrab runtime.
type Client struct {
	baseURL string // runtime base URL, no trailing slash
	ownerID string // trusted caller id for the runtime's REST messages API
	kbURL   string // omoikane's own base URL, embedded into agent instructions
	hc      *http.Client
	// talkHC serves the synchronous messages API (DispatchTalk): the
	// runtime runs the agent's full turn before answering, which can
	// legitimately take minutes, so no transport-level Timeout — the
	// caller's context is the only deadline.
	talkHC *http.Client
	// talkBackoff is DispatchTalk's first retry delay (doubled per
	// attempt). A field so tests can shrink it.
	talkBackoff time.Duration
}

// New builds a Client. kbURL is the omoikane base URL agents should call
// back to (typically cfg.OAuthRedirectBase).
func New(baseURL, ownerID, kbURL string) *Client {
	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		ownerID:     ownerID,
		kbURL:       strings.TrimRight(kbURL, "/"),
		hc:          &http.Client{Timeout: 30 * time.Second},
		talkHC:      &http.Client{},
		talkBackoff: time.Second,
	}
}

// DispatchTalk hands one /talk message to the agent's REST messages
// endpoint (POST /api/agents/{id}/messages). user_id is the client's
// trusted owner id — the same value Provision wrote into the agent's
// trust row, so the runtime resolves the caller as Owner and exposes
// the execution tools (opencrab caller_identity.rs).
//
// The endpoint is synchronous: it answers after the agent's whole turn.
// The response body here is only inspected for errors, never parsed for
// content: reply delivery lives on the gateway path (the agent's turn
// output arrives as the say) — since #132 the instructions carry no
// posting recipe, so this REST dispatch has no reply channel of its own
// and survives only as the pre-cutover-thread fallback (see
// internal/api/webhooks.go).
//
// Transient failures — connection errors and 5xx, i.e. the runtime was
// never reached or an infra layer failed in front of it (a restart
// blip) — are retried up to 3 attempts with exponential backoff, the
// same shape as omoikane's webhook delivery. 4xx and error-body
// responses are FINAL: the runtime processed the request, the agent's
// turn may already have run, and a re-send could run it twice.
func (c *Client) DispatchTalk(ctx context.Context, agentID, content string) error {
	if agentID == "" {
		return fmt.Errorf("opencrab: agent id required")
	}
	backoff := c.talkBackoff
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		err = c.do(ctx, c.talkHC, http.MethodPost,
			"/api/agents/"+agentID+"/messages",
			map[string]any{"user_id": c.ownerID, "content": content}, nil)
		var te *transientError
		if err == nil || !errors.As(err, &te) {
			return err
		}
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return err
			case <-time.After(backoff):
			}
			backoff *= 2
		}
	}
	return err
}

// transientError marks a failure worth retrying: the request never
// reached the runtime, or died in front of it, so re-sending cannot
// double-run an agent turn.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// httpError is a non-2xx runtime response. Typed so callers that must
// branch on the status — RuntimeSubjectResolver maps 404 to "not
// resolvable yet" — can errors.As for it; the message keeps the
// historical "HTTP <code>: <body>" shape.
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.status, e.body) }

// ProvisionParams is one provisioning request. All ids are generated
// server-side by the caller (never taken from a form).
type ProvisionParams struct {
	AgentID  string // "plib-<user_id>"
	UserName string // the owner's display name (embedded in instructions)
	Name     string // the librarian's name (user-chosen)
	Persona  string // free-text personality (user-chosen)
	KBToken  string // plaintext kb token to write into the workspace; "" = already issued, skip the write
}

// Provision drives the runtime API to the desired end state:
//
//  1. agent row exists (POST /api/agents when absent)
//  2. identity + instructions up to date (PUT /api/agents/{id})
//  3. trust row carries our owner id (PATCH or PUT /api/agents/{id}/discord,
//     verified by a follow-up GET — the PUT path reports a gateway
//     start-decline as an error even though the row was written)
//  4. kb credentials in the workspace (PUT /api/agents/{id}/workspace/.kb.curlrc)
//     — only when KBToken is non-empty (first provision)
//
// Errors name the failed step so the settings page can show where the
// pipeline stopped.
func (c *Client) Provision(ctx context.Context, p ProvisionParams) error {
	if p.AgentID == "" || p.Name == "" {
		return fmt.Errorf("opencrab: agent id and name required")
	}

	// Step 1 — ensure the agent row exists.
	exists, err := c.agentExists(ctx, p.AgentID)
	if err != nil {
		return fmt.Errorf("agent lookup (GET /api/agents/%s): %w", p.AgentID, err)
	}
	if !exists {
		if err := c.call(ctx, http.MethodPost, "/api/agents", map[string]any{
			"id":           p.AgentID,
			"name":         p.Name,
			"persona_name": p.Name,
		}, nil); err != nil {
			return fmt.Errorf("agent create (POST /api/agents): %w", err)
		}
	}

	// Step 2 — identity + instructions (common template + persona).
	if err := c.call(ctx, http.MethodPut, "/api/agents/"+p.AgentID, map[string]any{
		"name":         p.Name,
		"persona_name": p.Name,
		"instructions": Instructions(p.Name, p.UserName, p.Persona, c.kbURL),
	}, nil); err != nil {
		return fmt.Errorf("agent update (PUT /api/agents/%s): %w", p.AgentID, err)
	}

	// Step 3 — trust row (owner id only; no bot token, so the discord
	// gateway can never start for this agent).
	if err := c.ensureTrustRow(ctx, p.AgentID); err != nil {
		return err
	}

	// Step 4 — kb credentials into the workspace (first provision only).
	if p.KBToken != "" {
		if err := c.call(ctx, http.MethodPut,
			"/api/agents/"+p.AgentID+"/workspace/.kb.curlrc",
			map[string]any{"content": curlrc(p.KBToken)}, nil); err != nil {
			return fmt.Errorf("workspace write (PUT .kb.curlrc): %w", err)
		}
	}
	return nil
}

// agentExists checks GET /api/agents/{id} — the handler answers the
// agent row as JSON, or JSON null when absent.
func (c *Client) agentExists(ctx context.Context, id string) (bool, error) {
	var out json.RawMessage
	if err := c.call(ctx, http.MethodGet, "/api/agents/"+id, nil, &out); err != nil {
		return false, err
	}
	trimmed := strings.TrimSpace(string(out))
	return trimmed != "" && trimmed != "null", nil
}

// ensureTrustRow makes the agent's discord-config row exist with our
// owner id. The row (not the gateway) is what the runtime's REST
// messages API checks for caller trust; bot_token stays empty and the
// gateway is never started.
//
// PATCH updates an existing row in place; a missing row can only be
// created via PUT, whose handler also tries to start the gateway and
// then reports the (expected, token-less) start-decline as an error —
// so success is judged by a follow-up GET showing the row with our
// owner id, not by the PUT response.
func (c *Client) ensureTrustRow(ctx context.Context, agentID string) error {
	path := "/api/agents/" + agentID + "/discord"

	var cfg struct {
		Configured     bool   `json:"configured"`
		OwnerDiscordID string `json:"owner_discord_id"`
	}
	if err := c.call(ctx, http.MethodGet, path, nil, &cfg); err != nil {
		return fmt.Errorf("trust row lookup (GET %s): %w", path, err)
	}

	if cfg.Configured {
		if err := c.call(ctx, http.MethodPatch, path, map[string]any{
			"owner_discord_id": c.ownerID,
		}, nil); err != nil {
			return fmt.Errorf("trust row update (PATCH %s): %w", path, err)
		}
		return nil
	}

	// Row absent — create via PUT, tolerating the start-decline error.
	putErr := c.call(ctx, http.MethodPut, path, map[string]any{
		"bot_token":        "",
		"owner_discord_id": c.ownerID,
	}, nil)

	var after struct {
		Configured     bool   `json:"configured"`
		OwnerDiscordID string `json:"owner_discord_id"`
	}
	if err := c.call(ctx, http.MethodGet, path, nil, &after); err != nil {
		return fmt.Errorf("trust row verify (GET %s): %w", path, err)
	}
	if !after.Configured || after.OwnerDiscordID != c.ownerID {
		if putErr != nil {
			return fmt.Errorf("trust row create (PUT %s): %w", path, putErr)
		}
		return fmt.Errorf("trust row create (PUT %s): row not persisted (configured=%v owner=%q want %q)",
			path, after.Configured, after.OwnerDiscordID, c.ownerID)
	}
	return nil
}

// call issues one JSON request on the default (30s) client. Most of the
// provisioning API is quick request/response; only DispatchTalk needs
// the unbounded client.
func (c *Client) call(ctx context.Context, method, path string, body, into any) error {
	return c.do(ctx, c.hc, method, path, body, into)
}

// do issues one JSON request and decodes the response. opencrab
// handlers signal failure as HTTP 200 + {"error": "..."}, so the body
// is always inspected for an error key before decoding into `into`.
func (c *Client) do(ctx context.Context, hc *http.Client, method, path string, body, into any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		// A timeout while awaiting the response is FINAL, not
		// transient: the request may have reached the runtime and the
		// agent turn may already be running (the messages endpoint is
		// synchronous over the whole turn), so a re-send would run the
		// turn twice — issue #79 was three identical replies from
		// exactly this. The reply arrives out-of-band via the recipe,
		// so nothing is lost by giving up on the response body.
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			return err
		}
		// Other transport failures (connection refused, DNS): the
		// request never got out — retry-safe (see transientError).
		return &transientError{err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		err := &httpError{status: resp.StatusCode, body: strings.TrimSpace(string(raw))}
		if resp.StatusCode >= 500 {
			// 5xx = an infra layer failed in front of the handler
			// (opencrab itself reports failures as 200 + error body) —
			// retry-safe.
			return &transientError{err}
		}
		return err
	}
	var probe struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &probe) == nil && probe.Error != "" {
		return fmt.Errorf("runtime error: %s", probe.Error)
	}
	if into != nil && len(raw) > 0 {
		return json.Unmarshal(raw, into)
	}
	return nil
}

// curlrc is the workspace credentials file: `curl -K .kb.curlrc` adds
// the Authorization header on every kb call, keeping the plaintext
// token out of the agent's instructions and chat transcripts.
func curlrc(token string) string {
	return "# omoikane personal librarian credentials (auto-provisioned; do not edit)\n" +
		"header = \"Authorization: Bearer " + token + "\"\n"
}
