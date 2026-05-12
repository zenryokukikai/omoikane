package api

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// ============================================================
// Phase 6: tier listing + anomaly scan
// ============================================================

func (h *Handler) tierList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tier, _ := strconv.Atoi(q.Get("tier"))
	if tier == 0 {
		tier = 3
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	rows, err := h.Store.ListEntriesByTier(httpCtx(r), tier, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tier": tier, "entries": rows})
}

func (h *Handler) coordinatorTriage(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	minutes, _ := strconv.Atoi(r.URL.Query().Get("missing_heartbeat_minutes"))
	out, err := h.Store.CoordinatorAnomalyScan(httpCtx(r), minutes)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type proposeQuartetRequest struct {
	Topic    string `json:"topic"`
	ThreadID string `json:"thread_id,omitempty"`
}

func (h *Handler) coordinatorProposeQuartet(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfEmergencyStop(w) {
		return
	}
	var req proposeQuartetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.Topic == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "topic required", nil)
		return
	}
	q, err := h.Store.ProposeQuartet(httpCtx(r), req.Topic, req.ThreadID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, q)
}

// ============================================================
// Phase 7: backup + dead-pool + LLM usage + coverage
// ============================================================

type backupRequest struct {
	Path string `json:"path"`
}

func (h *Handler) adminBackup(w http.ResponseWriter, r *http.Request) {
	var req backupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "path required", nil)
		return
	}
	job, err := h.Store.RunBackup(httpCtx(r), req.Path)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (h *Handler) adminBackupList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	jobs, err := h.Store.ListBackups(httpCtx(r), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (h *Handler) adminDeadPool(w http.ResponseWriter, r *http.Request) {
	n, err := h.Store.ArchiveDormantEntries(httpCtx(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"archived": n})
}

func (h *Handler) adminLLMUsage(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	stats, err := h.Store.LLMUsageStatsWindow(httpCtx(r), days)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (h *Handler) adminCoverage(w http.ResponseWriter, r *http.Request) {
	stats, err := h.Store.HealthCoverageStats(httpCtx(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
