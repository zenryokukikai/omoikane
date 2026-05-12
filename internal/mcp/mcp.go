// Package mcp implements a minimal stdio Model Context Protocol server
// for omoikane. It exposes the lookup / search / read / write tools
// described in docs/design.md §8.1 by proxying each tool call to the Core
// HTTP API (so the MCP server can run in a separate process and remain a
// thin adapter).
//
// Wire format: newline-delimited JSON-RPC 2.0. Implements just enough of
// the MCP spec (initialize / tools/list / tools/call) for Claude Code,
// OpenCode, and other current clients.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const protocolVersion = "2024-11-05"

// Server runs the JSON-RPC dispatch loop over the given stdin/stdout.
type Server struct {
	CoreURL    string
	Token      string
	ProjectID  string // optional default
	HTTPClient *http.Client
}

// Run reads JSON-RPC messages from r and writes responses to w until r
// closes or the peer sends `shutdown`. Errors during a single message
// don't terminate the loop — they surface as JSON-RPC error responses.
func (s *Server) Run(ctx context.Context, r io.Reader, w io.Writer) error {
	if s.HTTPClient == nil {
		s.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	enc := json.NewEncoder(w)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(rpcResponse{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
			})
			continue
		}
		resp := s.dispatch(ctx, &req)
		if resp == nil {
			continue // notification, no response
		}
		_ = enc.Encode(resp)
	}
	return scanner.Err()
}

// ---- JSON-RPC envelope ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// dispatch routes a single JSON-RPC method to its handler. Returns nil
// when no response should be sent (e.g. for notifications).
func (s *Server) dispatch(ctx context.Context, req *rpcRequest) *rpcResponse {
	isNotification := len(req.ID) == 0
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "initialized", "notifications/initialized":
		// Notification: no response.
		return nil
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	case "shutdown", "notifications/cancelled":
		if isNotification {
			return nil
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	}
	if isNotification {
		return nil
	}
	return &rpcResponse{
		JSONRPC: "2.0", ID: req.ID,
		Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method},
	}
}

// ---- initialize ----

func (s *Server) handleInitialize(req *rpcRequest) *rpcResponse {
	return &rpcResponse{
		JSONRPC: "2.0", ID: req.ID,
		Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "omoikane-kb",
				"version": "0.1.0",
			},
		},
	}
}

// ---- tools/list ----

// toolDef describes a tool we expose. Schemas are kept inline (small) so
// agents don't need a follow-up call.
type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (s *Server) toolDefinitions() []toolDef {
	return []toolDef{
		{
			Name:        "kb_lookup_by_trigger",
			Description: "Find knowledge entries relevant to a planned action. Use BEFORE making changes to check for known traps. Returns entries with their `prohibited` field; the agent MUST check planned changes against these.",
			InputSchema: schemaObject(map[string]schemaProp{
				"trigger_description": {Type: "string", Description: "What you are about to do, in plain language."},
				"domain":              {Type: "string", Description: "Optional domain filter: preprocessing|training|inference|data|infra|other"},
				"top_k":               {Type: "integer", Description: "Max results (default 5)"},
				"project_id":          {Type: "string", Description: "Optional project filter"},
			}, []string{"trigger_description"}),
		},
		{
			Name:        "kb_lookup_by_symptom",
			Description: "Find knowledge entries that match an observed symptom. Use when diagnosing user-reported problems or unexpected behaviour.",
			InputSchema: schemaObject(map[string]schemaProp{
				"symptom_description": {Type: "string", Description: "The observed symptom, in plain language."},
				"top_k":               {Type: "integer"},
				"project_id":          {Type: "string"},
			}, []string{"symptom_description"}),
		},
		{
			Name:        "kb_lookup_by_tags",
			Description: "Find knowledge entries by tag membership. Use to browse by topic.",
			InputSchema: schemaObject(map[string]schemaProp{
				"tags":       {Type: "array", Description: "List of tags to match.", Items: &schemaProp{Type: "string"}},
				"match_mode": {Type: "string", Description: "any (default) or all"},
				"top_k":      {Type: "integer"},
				"project_id": {Type: "string"},
			}, []string{"tags"}),
		},
		{
			Name:        "kb_search",
			Description: "Full-text search across all knowledge entries.",
			InputSchema: schemaObject(map[string]schemaProp{
				"query": {Type: "string"},
				"top_k": {Type: "integer"},
			}, []string{"query"}),
		},
		{
			Name:        "kb_get",
			Description: "Fetch a single knowledge entry by ID.",
			InputSchema: schemaObject(map[string]schemaProp{
				"entry_id": {Type: "string"},
				"as_of":    {Type: "string", Description: "Optional RFC3339 timestamp for historical reconstruction."},
			}, []string{"entry_id"}),
		},
		{
			Name:        "kb_post",
			Description: "Create a new knowledge entry. Use AFTER solving a hard problem (type='trap') or AFTER hitting an unexplained failure (type='incident').",
			InputSchema: schemaObject(map[string]schemaProp{
				"project_id": {Type: "string"},
				"type":       {Type: "string", Description: "trap|decision|design|lesson|incident"},
				"title":      {Type: "string"},
				"body":       {Type: "string"},
				"symptom":    {Type: "string"},
				"root_cause": {Type: "string"},
				"resolution": {Type: "string"},
				"prohibited": {Type: "string"},
				"tags":       {Type: "array", Items: &schemaProp{Type: "string"}},
			}, []string{"project_id", "type", "title", "body"}),
		},
		{
			Name:        "kb_lookup_by_situation",
			Description: "Find knowledge entries linked to a described situation (reverse-dictionary lookup). Use when you only know roughly what you're working on but not the precise trigger or symptom.",
			InputSchema: schemaObject(map[string]schemaProp{
				"situation_description": {Type: "string", Description: "Free-form description of the current situation."},
				"top_k":                 {Type: "integer"},
				"project_id":            {Type: "string"},
				"create_cases":          {Type: "boolean", Description: "Record a usage_case per match so outcomes can be reported later."},
			}, []string{"situation_description"}),
		},
		{
			Name:        "kb_feedback",
			Description: "Record or update the outcome/result of having consulted a KB entry. Use AFTER acting on a lookup hit to close the feedback loop. Pass `case_id` to update an existing case; omit it to create a fresh case.",
			InputSchema: schemaObject(map[string]schemaProp{
				"case_id":         {Type: "string", Description: "Existing case_id to update. If omitted, requires entry_id."},
				"entry_id":        {Type: "string", Description: "Required for new cases; ignored when case_id is set."},
				"trigger_query":   {Type: "string"},
				"outcome":         {Type: "string", Description: "applied|considered_rejected|ignored"},
				"result":          {Type: "string", Description: "helpful|partially_helpful|not_helpful|misleading|unknown"},
				"result_evidence": {Type: "string"},
				"notes":           {Type: "string"},
			}, []string{}),
		},
		{
			Name:        "kb_link",
			Description: "Create a directed relation between two entries. Valid rel_type values: related|supersedes|conflicts_with|depends_on|see_also|duplicate_of|resolved_by. Adding conflicts_with auto-supersedes the older entry.",
			InputSchema: schemaObject(map[string]schemaProp{
				"from_id":    {Type: "string"},
				"to_id":      {Type: "string"},
				"rel_type":   {Type: "string"},
				"confidence": {Type: "number"},
				"notes":      {Type: "string"},
			}, []string{"from_id", "to_id", "rel_type"}),
		},
		{
			Name:        "kb_relations",
			Description: "List relations for a given entry. `direction` is outgoing|incoming|both (default outgoing).",
			InputSchema: schemaObject(map[string]schemaProp{
				"entry_id":  {Type: "string"},
				"direction": {Type: "string"},
			}, []string{"entry_id"}),
		},
		{
			Name:        "kb_browse",
			Description: "Walk the hierarchy. Without `node_id` returns root nodes; with `node_id` returns the node, its children, and (when include_entries=true) the entries attached to it.",
			InputSchema: schemaObject(map[string]schemaProp{
				"node_id":         {Type: "string"},
				"include_entries": {Type: "boolean"},
				"project_id":      {Type: "string"},
			}, []string{}),
		},
		{
			Name:        "kb_reflect",
			Description: "Cross-entry reflection: pass an array of entry_ids plus an optional prompt, get back a summary. Useful when you need to compare multiple traps / decisions / lessons before acting.",
			InputSchema: schemaObject(map[string]schemaProp{
				"entry_ids": {Type: "array", Items: &schemaProp{Type: "string"}},
				"prompt":    {Type: "string"},
			}, []string{"entry_ids"}),
		},
	}
}

type schemaProp struct {
	Type        string      `json:"type"`
	Description string      `json:"description,omitempty"`
	Items       *schemaProp `json:"items,omitempty"`
}

func schemaObject(props map[string]schemaProp, required []string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func (s *Server) handleToolsList(req *rpcRequest) *rpcResponse {
	return &rpcResponse{
		JSONRPC: "2.0", ID: req.ID,
		Result: map[string]any{"tools": s.toolDefinitions()},
	}
}

// ---- tools/call ----

type toolsCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

func (s *Server) handleToolsCall(ctx context.Context, req *rpcRequest) *rpcResponse {
	var p toolsCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return rpcErr(req.ID, -32602, "invalid params: "+err.Error())
	}
	// Per tool, build the Core HTTP request and proxy the response back
	// as a text "content" block.
	var (
		body any
		path string
		meth string
	)
	switch p.Name {
	case "kb_lookup_by_trigger":
		path, meth = "/v1/lookup/by-trigger", http.MethodPost
		body = mergeProject(p.Arguments, s.ProjectID)
	case "kb_lookup_by_symptom":
		path, meth = "/v1/lookup/by-symptom", http.MethodPost
		body = mergeProject(p.Arguments, s.ProjectID)
	case "kb_lookup_by_tags":
		path, meth = "/v1/lookup/by-tags", http.MethodPost
		body = mergeProject(p.Arguments, s.ProjectID)
	case "kb_search":
		path, meth = "/v1/search", http.MethodPost
		body = p.Arguments
	case "kb_get":
		eid, _ := p.Arguments["entry_id"].(string)
		if eid == "" {
			return rpcErr(req.ID, -32602, "entry_id required")
		}
		q := ""
		if asOf, ok := p.Arguments["as_of"].(string); ok && asOf != "" {
			q = "?as_of=" + asOf
		}
		path, meth, body = "/v1/entries/"+eid+q, http.MethodGet, nil
	case "kb_post":
		path, meth = "/v1/entries", http.MethodPost
		body = mergeProject(p.Arguments, s.ProjectID)
	case "kb_lookup_by_situation":
		path, meth = "/v1/lookup/by-situation", http.MethodPost
		body = mergeProject(p.Arguments, s.ProjectID)
	case "kb_feedback":
		// case_id present → PATCH /v1/cases/{id}; otherwise POST /v1/cases.
		if cid, ok := p.Arguments["case_id"].(string); ok && cid != "" {
			args := make(map[string]any, len(p.Arguments))
			for k, v := range p.Arguments {
				if k == "case_id" || k == "entry_id" || k == "trigger_query" {
					continue
				}
				args[k] = v
			}
			path, meth, body = "/v1/cases/"+cid, http.MethodPatch, args
		} else {
			path, meth = "/v1/cases", http.MethodPost
			body = mergeProject(p.Arguments, s.ProjectID)
		}
	case "kb_link":
		path, meth = "/v1/relations", http.MethodPost
		body = p.Arguments
	case "kb_relations":
		eid, _ := p.Arguments["entry_id"].(string)
		if eid == "" {
			return rpcErr(req.ID, -32602, "entry_id required")
		}
		direction, _ := p.Arguments["direction"].(string)
		q := ""
		if direction != "" {
			q = "?direction=" + direction
		}
		path, meth, body = "/v1/entries/"+eid+"/relations"+q, http.MethodGet, nil
	case "kb_browse":
		nodeID, _ := p.Arguments["node_id"].(string)
		includeEntries, _ := p.Arguments["include_entries"].(bool)
		if nodeID == "" {
			path, meth, body = "/v1/browse", http.MethodGet, nil
		} else if includeEntries {
			path, meth, body = "/v1/browse/"+nodeID+"/entries", http.MethodGet, nil
		} else {
			path, meth, body = "/v1/browse/"+nodeID, http.MethodGet, nil
		}
	case "kb_reflect":
		path, meth = "/v1/reflect", http.MethodPost
		body = p.Arguments
	default:
		return rpcErr(req.ID, -32602, "unknown tool: "+p.Name)
	}

	raw, status, err := s.proxy(ctx, meth, path, body)
	if err != nil {
		// Fail-open: return the error as a content block but DON'T error
		// the JSON-RPC call so agents can keep going (design.md §18.4).
		return &rpcResponse{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{
				"content": []map[string]any{{
					"type": "text",
					"text": fmt.Sprintf(`{"error":%q,"kb_unavailable":true}`, err.Error()),
				}},
				"isError": false,
			},
		}
	}
	isError := status >= 400
	return &rpcResponse{
		JSONRPC: "2.0", ID: req.ID,
		Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(raw)}},
			"isError": isError,
		},
	}
}

// proxy issues the HTTP call and returns the response body verbatim. Any
// transport-level error is returned so the caller can surface a
// kb_unavailable flag.
func (s *Server) proxy(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.CoreURL, "/")+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("X-Client-Type", "mcp-stdio")
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return raw, resp.StatusCode, nil
}

func mergeProject(args map[string]any, project string) map[string]any {
	if project == "" {
		return args
	}
	if _, ok := args["project_id"]; ok {
		return args
	}
	out := make(map[string]any, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	out["project_id"] = project
	return out
}

func rpcErr(id json.RawMessage, code int, msg string) *rpcResponse {
	return &rpcResponse{
		JSONRPC: "2.0", ID: id,
		Error: &rpcError{Code: code, Message: msg},
	}
}
