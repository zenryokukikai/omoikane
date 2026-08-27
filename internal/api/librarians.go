package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
)

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
			"role must be one of coordinator|cataloger|curator|detective|conservator|scout|indexer|summarizer|judge",
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
//
//	role        (required) — librarian role
//	project_id  (optional) — restrict backlog to one project
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

// reprocessRequest clears progress for specific entries so a role
// re-processes them. Maintenance primitive (e.g. re-summarise after a
// template change). entry_ids is required and bounded — never clears a
// whole role's history.
type reprocessRequest struct {
	Role     string   `json:"role"`
	EntryIDs []string `json:"entry_ids"`
}

func (h *Handler) librarianBacklogReprocess(w http.ResponseWriter, r *http.Request) {
	var req reprocessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if !store.ValidLibrarianRole(req.Role) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "role not recognised",
			map[string]any{"got": req.Role, "allowed": store.LibrarianRoleSlice()})
		return
	}
	if len(req.EntryIDs) == 0 {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "entry_ids required (non-empty)", nil)
		return
	}
	n, err := h.Store.ClearProgress(httpCtx(r), req.Role, req.EntryIDs)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": n, "role": req.Role})
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
