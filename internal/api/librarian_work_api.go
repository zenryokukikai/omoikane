package api

// Librarian work queues: /v1/librarian/tasks, /v1/librarian/quartet
// and /v1/librarian/findings.

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/store"
)

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
