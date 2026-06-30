package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// ============================================================
// /v1/open_work — agent-first interface for autonomous pick-up
//
// Per the agent-first design principle (entry X-SQATAB) this lives in
// REST + MCP + CLI; there is intentionally no dashboard surface yet.
// Humans inspect via the existing entries list filtered by tag=open.
// ============================================================

func (h *Handler) listOpenWork(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := h.Store.ListOpenWork(httpCtx(r), q.Get("role"), q.Get("effort"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type claimOpenWorkRequest struct {
	Role       string `json:"role"`
	InstanceID string `json:"instance_id"`
	Effort     string `json:"effort,omitempty"`
}

func (h *Handler) claimOpenWork(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	entryID := chi.URLParam(r, "id")
	var req claimOpenWorkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.Role == "" || req.InstanceID == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields,
			"role and instance_id are required", nil)
		return
	}
	taskID, err := h.Store.ClaimOpenWork(httpCtx(r), entryID, req.Role, req.InstanceID, req.Effort)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"task_id":  taskID,
		"entry_id": entryID,
		"claimed_by": req.InstanceID,
	})
}

type releaseOpenWorkRequest struct {
	InstanceID string `json:"instance_id"`
}

func (h *Handler) releaseOpenWork(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	entryID := chi.URLParam(r, "id")
	var req releaseOpenWorkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.InstanceID == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields,
			"instance_id required", nil)
		return
	}
	if err := h.Store.ReleaseOpenWork(httpCtx(r), entryID, req.InstanceID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type mergeOpenWorkRequest struct {
	InstanceID  string `json:"instance_id"`
	Result      string `json:"result,omitempty"`
	ImplEntryID string `json:"impl_entry_id,omitempty"`
}

func (h *Handler) mergeOpenWork(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	entryID := chi.URLParam(r, "id")
	var req mergeOpenWorkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.InstanceID == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields,
			"instance_id required", nil)
		return
	}
	if err := h.Store.MarkOpenWorkMerged(httpCtx(r), entryID, req.InstanceID,
		req.Result, req.ImplEntryID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// satisfy 'unused' lint when build tags hide things — store import would
// otherwise drift in a refactor:
var _ = store.OpenWorkItem{}
