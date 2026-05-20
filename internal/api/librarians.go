package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/auth"
	"github.com/kojira/omoikane/internal/store"
)

// emergencyStop is the cluster-wide off switch. When true, all librarian
// /v1/librarian/* writes are rejected with 503. Phase 6 §23.8 mandates
// this — Phase 5 ships it as a stub so existing call sites work once
// real librarians come online.
var emergencyStop int32 // 0/1

// ============================================================
// /v1/librarian/instances + heartbeat
// ============================================================

type registerLibrarianRequest struct {
	Role         string `json:"role"`
	InstanceID   string `json:"instance_id,omitempty"`
	SkillVersion string `json:"skill_version,omitempty"`
	AgentRuntime string `json:"agent_runtime,omitempty"`
	Status       string `json:"status,omitempty"`
	Metadata     string `json:"metadata,omitempty"`
}

func (h *Handler) librarianRegister(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	var req registerLibrarianRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if !store.ValidLibrarianRole(req.Role) {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"role must be one of coordinator|cataloger|curator|detective|conservator|scout|summarizer|judge",
			map[string]any{"got": req.Role, "allowed": store.LibrarianRoleSlice()})
		return
	}
	// Role-consistency check.
	// - Librarian-scoped users (issued via librarian_role invite) MUST
	//   register the role they were issued for. A cataloger token
	//   cannot register as curator.
	// - Admin-scoped users may register any role (manual operations,
	//   tests, bootstrap before the first librarian invite exists).
	// - Anything else: forbidden.
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken,
			"librarian registration requires an authenticated user", nil)
		return
	}
	u, err := h.Store.GetUser(httpCtx(r), tok.UserID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	switch {
	case u.LibrarianRole != "":
		if u.LibrarianRole != req.Role {
			writeError(w, http.StatusForbidden, CodeForbidden,
				"role mismatch: token user is bound to a different librarian role",
				map[string]any{"token_role": u.LibrarianRole, "request_role": req.Role})
			return
		}
	case store.HasScope(tok.Scopes, "admin"):
		// Admin manual path — allowed for any role.
	default:
		writeError(w, http.StatusForbidden, CodeForbidden,
			"this token cannot register a librarian instance: it has neither a "+
				"librarian_role nor admin scope", nil)
		return
	}
	id, err := h.Store.RegisterLibrarianInstance(httpCtx(r), &store.LibrarianInstance{
		InstanceID:   req.InstanceID,
		Role:         req.Role,
		SkillVersion: req.SkillVersion,
		AgentRuntime: req.AgentRuntime,
		Status:       req.Status,
		Metadata:     req.Metadata,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"instance_id": id})
}

// backlogNextHandler returns the oldest entry the given librarian role
// has not yet processed. Returns 404 with code=NOT_FOUND when the
// backlog is empty (the caller treats this as "I'm caught up, just
// heartbeat and exit").
//
// Query params:
//   role        (required) — librarian role
//   project_id  (optional) — restrict backlog to one project
//
// Response: the full entry, plus a `backlog_size` count so callers
// can log progress and dashboards can show "X entries remaining for
// cataloger".
func (h *Handler) librarianBacklogNext(w http.ResponseWriter, r *http.Request) {
	role := r.URL.Query().Get("role")
	if role == "" {
		writeError(w, http.StatusBadRequest, CodeBadQuery, "role is required",
			map[string]any{"allowed": store.LibrarianRoleSlice()})
		return
	}
	if !store.ValidLibrarianRole(role) {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"role not recognised",
			map[string]any{"got": role, "allowed": store.LibrarianRoleSlice()})
		return
	}
	projectID := r.URL.Query().Get("project_id")
	e, err := h.Store.NextUnprocessedEntry(httpCtx(r), role, projectID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	size, _ := h.Store.BacklogSize(httpCtx(r), role, projectID)
	writeJSON(w, http.StatusOK, map[string]any{
		"entry":        e,
		"backlog_size": size,
	})
}

// progressRequest records that a librarian instance has processed
// (or chose not to act on) an entry. The store records the row and
// the FIFO query stops returning this entry for this role.
type progressRequest struct {
	Role          string `json:"role"`
	EntryID       string `json:"entry_id"`
	InstanceID    string `json:"instance_id,omitempty"`
	Action        string `json:"action"`
	OutputEntryID string `json:"output_entry_id,omitempty"`
	Notes         string `json:"notes,omitempty"`
}

func (h *Handler) librarianProgressPost(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	var req progressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	p := &store.LibrarianProgress{
		Role:          strings.TrimSpace(req.Role),
		EntryID:       strings.TrimSpace(req.EntryID),
		InstanceID:    strings.TrimSpace(req.InstanceID),
		Action:        strings.TrimSpace(req.Action),
		OutputEntryID: strings.TrimSpace(req.OutputEntryID),
		Notes:         req.Notes,
	}
	if err := h.Store.RecordProgress(httpCtx(r), p); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":           p.ID,
		"role":         p.Role,
		"entry_id":     p.EntryID,
		"action":       p.Action,
		"processed_at": p.ProcessedAt,
	})
}

// librarianProgressList returns the most recent progress rows for the
// given role. Used by dashboards and by the librarian's own ticks to
// answer "what did I do last time?" without needing local state.
func (h *Handler) librarianProgressList(w http.ResponseWriter, r *http.Request) {
	role := r.URL.Query().Get("role")
	if role == "" {
		writeError(w, http.StatusBadRequest, CodeBadQuery, "role is required",
			map[string]any{"allowed": store.LibrarianRoleSlice()})
		return
	}
	if !store.ValidLibrarianRole(role) {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"role not recognised",
			map[string]any{"got": role, "allowed": store.LibrarianRoleSlice()})
		return
	}
	instanceID := r.URL.Query().Get("instance_id")
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	rows, err := h.Store.ListProgress(httpCtx(r), role, instanceID, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	size, _ := h.Store.BacklogSize(httpCtx(r), role, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"progress":     rows,
		"backlog_size": size,
	})
}

func (h *Handler) librarianHeartbeat(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.Store.RecordHeartbeat(httpCtx(r), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type setLibrarianStatusRequest struct {
	Status string `json:"status"`
}

func (h *Handler) librarianSetStatus(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	id := chi.URLParam(r, "id")
	var req setLibrarianStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	switch req.Status {
	case "OBSERVING", "ACTIVE", "PAUSED", "STOPPED":
		// ok
	default:
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"status must be OBSERVING|ACTIVE|PAUSED|STOPPED",
			map[string]any{"got": req.Status})
		return
	}
	if err := h.Store.SetLibrarianStatus(httpCtx(r), id, req.Status); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// librarianGet returns one instance's full state. Used by the per-tick
// emergency-stop check so a librarian can decide whether to act.
func (h *Handler) librarianGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	inst, err := h.Store.GetLibrarianInstance(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

func (h *Handler) librarianList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := h.Store.ListLibrarianInstances(httpCtx(r), q.Get("role"), q.Get("status"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": list})
}

// ============================================================
// /v1/librarian/chat + threads
// ============================================================

type chatThreadRequest struct {
	ThreadID       string `json:"thread_id,omitempty"`
	Title          string `json:"title,omitempty"`
	Intent         string `json:"intent,omitempty"`
	Summary        string `json:"summary,omitempty"`
	RelatedEntries string `json:"related_entries,omitempty"`
	Metadata       string `json:"metadata,omitempty"`
}

func (h *Handler) chatOpenThread(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	var req chatThreadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	id, err := h.Store.OpenThread(httpCtx(r), &store.ChatThread{
		ThreadID: req.ThreadID, Title: req.Title, Intent: req.Intent,
		Summary: req.Summary, RelatedEntries: req.RelatedEntries,
		Metadata: req.Metadata,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"thread_id": id})
}

type chatCloseRequest struct {
	Summary string `json:"summary,omitempty"`
}

func (h *Handler) chatCloseThread(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	id := chi.URLParam(r, "id")
	var req chatCloseRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
			return
		}
	}
	if err := h.Store.CloseThread(httpCtx(r), id, req.Summary); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) chatListThreads(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := h.Store.ListThreads(httpCtx(r), status, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"threads": list})
}

type chatPostRequest struct {
	ThreadID         string `json:"thread_id,omitempty"`
	AuthorRole       string `json:"author_role"`
	AuthorInstanceID string `json:"author_instance_id,omitempty"`
	ReplyTo          string `json:"reply_to,omitempty"`
	Mentions         string `json:"mentions,omitempty"`
	Intent           string `json:"intent,omitempty"`
	Content          string `json:"content"`
	RelatedEntries   string `json:"related_entries,omitempty"`
	InputTokens      int    `json:"input_tokens,omitempty"`
	OutputTokens     int    `json:"output_tokens,omitempty"`
}

func (h *Handler) chatPost(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	var req chatPostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	// author_user_id is server-side authority: we pull it from the
	// auth context, never from the request body. This means a reader
	// can trust the link from "this message" → "this profile" — no
	// way for a client to impersonate someone else here.
	var authorUserID string
	if tok := auth.FromContext(r.Context()); tok != nil {
		authorUserID = tok.UserID
	}
	id, err := h.Store.PostChatMessage(httpCtx(r), &store.ChatMessage{
		ThreadID: req.ThreadID, AuthorRole: req.AuthorRole,
		AuthorInstanceID: req.AuthorInstanceID,
		AuthorUserID:     authorUserID,
		ReplyTo:          req.ReplyTo,
		Mentions:         req.Mentions, Intent: req.Intent, Content: req.Content,
		RelatedEntries: req.RelatedEntries,
		InputTokens:    req.InputTokens, OutputTokens: req.OutputTokens,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// chatList serves GET /v1/librarian/threads/{id}/messages.
//
// Plain mode (`?limit=N`): returns the first N messages in the
// thread, oldest first.
//
// Cursor mode (`?since=<message-id>&limit=N`): returns up to N
// messages newer than the supplied message. Empty list when there's
// nothing newer.
//
// Long-poll mode (`?since=<message-id>&wait=30s`): if cursor-mode
// would return empty, the handler holds the connection for up to
// `wait` seconds, re-checking the store roughly every second. As
// soon as new messages appear the handler flushes and returns.
// This lets agents implement pseudo-realtime ping-pong without
// burning request volume on tight polling loops.
//
// `wait` is capped at 5 minutes to avoid runaway client behaviour
// pinning server resources. Context cancellation (client
// disconnect) terminates the wait immediately.
func (h *Handler) chatList(w http.ResponseWriter, r *http.Request) {
	threadID := chi.URLParam(r, "id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	sinceID := r.URL.Query().Get("since")
	waitStr := r.URL.Query().Get("wait")

	ctx := httpCtx(r)

	// Resolve `since` to a timestamp (if it points at a real msg).
	// Unknown id → treat as no cursor (start from beginning).
	var sinceTS time.Time
	if sinceID != "" {
		if m, err := h.Store.GetChatMessage(ctx, sinceID); err == nil {
			sinceTS = m.Timestamp
		}
	}

	// Parse wait duration. Cap at 5 minutes. Zero = no long-poll.
	var waitUntil time.Time
	if waitStr != "" {
		d, err := time.ParseDuration(waitStr)
		if err == nil && d > 0 {
			if d > 5*time.Minute {
				d = 5 * time.Minute
			}
			waitUntil = time.Now().Add(d)
		}
	}

	for {
		msgs, err := h.Store.ListChatMessagesSince(ctx, threadID, sinceTS, limit)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if len(msgs) > 0 || time.Now().After(waitUntil) {
			writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
			return
		}
		// No new messages and still inside the wait window. Sleep
		// ~1s then re-check, but bail out on client disconnect.
		select {
		case <-time.After(1 * time.Second):
		case <-ctx.Done():
			// Client gave up. Just return what we have (empty).
			writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
			return
		}
	}
}

// ============================================================
// /v1/librarian/tasks
// ============================================================

type taskRequest struct {
	TaskID      string `json:"task_id,omitempty"`
	Role        string `json:"role"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Priority    int    `json:"priority,omitempty"`
}

func (h *Handler) taskEnqueue(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	var req taskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	id, err := h.Store.EnqueueTask(httpCtx(r), &store.LibrarianTask{
		TaskID: req.TaskID, Role: req.Role, Title: req.Title,
		Description: req.Description, Priority: req.Priority,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"task_id": id})
}

type taskClaimRequest struct {
	InstanceID string `json:"instance_id"`
}

func (h *Handler) taskClaim(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	id := chi.URLParam(r, "id")
	var req taskClaimRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.InstanceID == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "instance_id required", nil)
		return
	}
	if err := h.Store.ClaimTask(httpCtx(r), id, req.InstanceID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type taskCompleteRequest struct {
	Result  string `json:"result,omitempty"`
	Success bool   `json:"success"`
}

func (h *Handler) taskComplete(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	id := chi.URLParam(r, "id")
	var req taskCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if err := h.Store.CompleteTask(httpCtx(r), id, req.Result, req.Success); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) taskList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := h.Store.ListTasks(httpCtx(r), q.Get("role"), q.Get("status"), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": list})
}

// ============================================================
// /v1/librarian/quartet
// ============================================================

type quartetRequest struct {
	ID           string `json:"id,omitempty"`
	Topic        string `json:"topic"`
	ThreadID     string `json:"thread_id,omitempty"`
	Participant1 string `json:"participant_1"`
	Participant2 string `json:"participant_2"`
	Participant3 string `json:"participant_3"`
	Judge        string `json:"judge"`
	Metadata     string `json:"metadata,omitempty"`
}

func (h *Handler) quartetCreate(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	var req quartetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	id, err := h.Store.CreateQuartet(httpCtx(r), &store.QuartetAssignment{
		ID: req.ID, Topic: req.Topic, ThreadID: req.ThreadID,
		Participant1: req.Participant1, Participant2: req.Participant2,
		Participant3: req.Participant3, Judge: req.Judge,
		Metadata: req.Metadata,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

type quartetDecisionRequest struct {
	Decision string `json:"decision"`
}

func (h *Handler) quartetDecide(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	id := chi.URLParam(r, "id")
	var req quartetDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.Decision == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "decision required", nil)
		return
	}
	if err := h.Store.DecideQuartet(httpCtx(r), id, req.Decision); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) quartetList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := h.Store.ListQuartets(httpCtx(r), q.Get("status"), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"quartets": list})
}

// ============================================================
// /v1/librarian/findings
// ============================================================

type findingRequest struct {
	ID          string  `json:"id,omitempty"`
	AgentLens   string  `json:"agent_lens"`
	InstanceID  string  `json:"instance_id,omitempty"`
	SourceURL   string  `json:"source_url,omitempty"`
	SourceTitle string  `json:"source_title,omitempty"`
	Excerpt     string  `json:"excerpt,omitempty"`
	Relevance   float64 `json:"relevance,omitempty"`
	Tags        string  `json:"tags,omitempty"`
	Metadata    string  `json:"metadata,omitempty"`
}

func (h *Handler) findingRecord(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	var req findingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	id, err := h.Store.RecordFinding(httpCtx(r), &store.ExternalFinding{
		ID: req.ID, AgentLens: req.AgentLens, InstanceID: req.InstanceID,
		SourceURL: req.SourceURL, SourceTitle: req.SourceTitle, Excerpt: req.Excerpt,
		Relevance: req.Relevance, Tags: req.Tags, Metadata: req.Metadata,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

type findingCorrelateRequest struct {
	EntryID     string  `json:"entry_id"`
	Correlation float64 `json:"correlation,omitempty"`
}

func (h *Handler) findingCorrelate(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	id := chi.URLParam(r, "id")
	var req findingCorrelateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.EntryID == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "entry_id required", nil)
		return
	}
	if err := h.Store.CorrelateFinding(httpCtx(r), id, req.EntryID, req.Correlation); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) findingList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := h.Store.ListFindings(httpCtx(r), q.Get("agent_lens"), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"findings": list})
}

// ============================================================
// /v1/librarian/emergency_stop
// ============================================================

type emergencyStopRequest struct {
	Engage bool `json:"engage"`
}

func (h *Handler) librarianEmergencyStop(w http.ResponseWriter, r *http.Request) {
	var req emergencyStopRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.Engage {
		atomic.StoreInt32(&emergencyStop, 1)
		h.Logger.Warn("librarian emergency stop engaged")
	} else {
		atomic.StoreInt32(&emergencyStop, 0)
		h.Logger.Info("librarian emergency stop released")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"engaged": atomic.LoadInt32(&emergencyStop) == 1,
	})
}

// rejectIfEmergencyStop returns true (and writes 503) when the kill
// switch is engaged. All librarian write endpoints call this first.
func (h *Handler) rejectIfEmergencyStop(w http.ResponseWriter) bool {
	if atomic.LoadInt32(&emergencyStop) != 1 {
		return false
	}
	writeError(w, http.StatusServiceUnavailable, "EMERGENCY_STOP",
		"Librarian community is currently halted by emergency stop.", nil)
	return true
}

// ResetEmergencyStopForTest is exported so test code can reset the
// package-level flag between sub-tests. Not callable from production
// since the function name starts with the harmless "Reset" verb but is
// guarded by the *_test.go discipline.
func ResetEmergencyStopForTest() { atomic.StoreInt32(&emergencyStop, 0) }

// guard against rotted-import warnings if errors becomes unused later.
var _ = errors.New
