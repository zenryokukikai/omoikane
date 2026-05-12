package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/store"
)

// ============================================================
// usage_cases (feedback loop)
// ============================================================

type caseRequest struct {
	EntryID           string `json:"entry_id"`
	ProjectID         string `json:"project_id,omitempty"`
	ClientType        string `json:"client_type,omitempty"`
	ClientVersion     string `json:"client_version,omitempty"`
	SessionID         string `json:"session_id,omitempty"`
	AgentRole         string `json:"agent_role,omitempty"`
	AgentLabel        string `json:"agent_label,omitempty"`
	TriggerQuery      string `json:"trigger_query,omitempty"`
	TaskContext       string `json:"task_context,omitempty"`
	Environment       string `json:"environment,omitempty"`
	Outcome           string `json:"outcome,omitempty"`
	ApplicationDetail string `json:"application_detail,omitempty"`
	RejectionReason   string `json:"rejection_reason,omitempty"`
	Result            string `json:"result,omitempty"`
	ResultEvidence    string `json:"result_evidence,omitempty"`
	Notes             string `json:"notes,omitempty"`
	Metadata          string `json:"metadata,omitempty"`
}

type casePatchRequest struct {
	Outcome           *string `json:"outcome,omitempty"`
	ApplicationDetail *string `json:"application_detail,omitempty"`
	RejectionReason   *string `json:"rejection_reason,omitempty"`
	Result            *string `json:"result,omitempty"`
	ResultEvidence    *string `json:"result_evidence,omitempty"`
	ResultJudgedBy    *string `json:"result_judged_by,omitempty"`
	Notes             *string `json:"notes,omitempty"`
}

func (h *Handler) createCase(w http.ResponseWriter, r *http.Request) {
	var req caseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.EntryID == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields,
			"entry_id is required", nil)
		return
	}
	c := &store.UsageCase{
		EntryID:           req.EntryID,
		ProjectID:         req.ProjectID,
		ClientType:        req.ClientType,
		ClientVersion:     req.ClientVersion,
		SessionID:         req.SessionID,
		AgentRole:         req.AgentRole,
		AgentLabel:        req.AgentLabel,
		TriggerQuery:      req.TriggerQuery,
		TaskContext:       req.TaskContext,
		Environment:       req.Environment,
		Outcome:           req.Outcome,
		ApplicationDetail: req.ApplicationDetail,
		RejectionReason:   req.RejectionReason,
		Result:            req.Result,
		ResultEvidence:    req.ResultEvidence,
		Notes:             req.Notes,
		Metadata:          req.Metadata,
	}
	id, err := h.Store.CreateCase(httpCtx(r), c)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	c.CaseID = id
	writeJSON(w, http.StatusCreated, c)
}

func (h *Handler) patchCase(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req casePatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	c, err := h.Store.PatchCase(httpCtx(r), id, store.CasePatch{
		Outcome:           req.Outcome,
		ApplicationDetail: req.ApplicationDetail,
		RejectionReason:   req.RejectionReason,
		Result:            req.Result,
		ResultEvidence:    req.ResultEvidence,
		ResultJudgedBy:    req.ResultJudgedBy,
		Notes:             req.Notes,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *Handler) getCase(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := h.Store.GetCase(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *Handler) listEntryCases(w http.ResponseWriter, r *http.Request) {
	entryID := chi.URLParam(r, "id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	cases, err := h.Store.ListCases(httpCtx(r), entryID, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cases": cases})
}

func (h *Handler) entrySignals(w http.ResponseWriter, r *http.Request) {
	entryID := chi.URLParam(r, "id")
	sig, err := h.Store.EntrySignal(httpCtx(r), entryID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sig)
}

func (h *Handler) reviewQueue(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := h.Store.ReviewQueue(httpCtx(r), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": rows})
}

// ============================================================
// relations
// ============================================================

type relationRequest struct {
	FromID     string  `json:"from_id"`
	ToID       string  `json:"to_id"`
	RelType    string  `json:"rel_type"`
	Confidence float64 `json:"confidence,omitempty"`
	Source     string  `json:"source,omitempty"`
	Notes      string  `json:"notes,omitempty"`
}

func (h *Handler) createRelation(w http.ResponseWriter, r *http.Request) {
	var req relationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.FromID == "" || req.ToID == "" || req.RelType == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields,
			"from_id, to_id, rel_type are required", nil)
		return
	}
	if !store.ValidRelType(req.RelType) {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"invalid rel_type", map[string]any{"got": req.RelType})
		return
	}
	rel := &store.Relation{
		FromID:     req.FromID,
		ToID:       req.ToID,
		RelType:    req.RelType,
		Confidence: req.Confidence,
		Source:     req.Source,
		Notes:      req.Notes,
	}
	if err := h.Store.CreateRelation(httpCtx(r), rel); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rel)
}

func (h *Handler) deleteRelation(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fromID := q.Get("from_id")
	toID := q.Get("to_id")
	relType := q.Get("rel_type")
	if fromID == "" || toID == "" || relType == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields,
			"from_id, to_id, rel_type query params required", nil)
		return
	}
	if err := h.Store.DeleteRelation(httpCtx(r), fromID, toID, relType); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listEntryRelations(w http.ResponseWriter, r *http.Request) {
	entryID := chi.URLParam(r, "id")
	direction := r.URL.Query().Get("direction")
	var (
		rels []*store.Relation
		err  error
	)
	switch direction {
	case "incoming":
		rels, err = h.Store.ListRelationsTo(httpCtx(r), entryID)
	case "both":
		out, e := h.Store.ListRelationsFrom(httpCtx(r), entryID)
		if e != nil {
			writeStoreError(w, e)
			return
		}
		in, e := h.Store.ListRelationsTo(httpCtx(r), entryID)
		if e != nil {
			writeStoreError(w, e)
			return
		}
		rels = append(out, in...)
	default:
		rels, err = h.Store.ListRelationsFrom(httpCtx(r), entryID)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"relations": rels})
}

// ============================================================
// situations
// ============================================================

type situationRequest struct {
	ID          string  `json:"id,omitempty"`
	ProjectID   string  `json:"project_id,omitempty"`
	Description string  `json:"description"`
	Domain      string  `json:"domain,omitempty"`
	Metadata    string  `json:"metadata,omitempty"`
	EntryIDs    []entry `json:"entries,omitempty"`
}

type entry struct {
	EntryID   string  `json:"entry_id"`
	Relevance float64 `json:"relevance,omitempty"`
	Notes     string  `json:"notes,omitempty"`
}

func (h *Handler) createSituation(w http.ResponseWriter, r *http.Request) {
	var req situationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.Description == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields,
			"description is required", nil)
		return
	}
	sit := &store.Situation{
		ID:          req.ID,
		ProjectID:   req.ProjectID,
		Description: req.Description,
		Domain:      req.Domain,
		Metadata:    req.Metadata,
	}
	id, err := h.Store.CreateSituation(httpCtx(r), sit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	sit.ID = id
	for _, e := range req.EntryIDs {
		if err := h.Store.LinkEntryToSituation(httpCtx(r), id, e.EntryID, e.Relevance, e.Notes); err != nil {
			h.Logger.Warn("link entry to situation failed", "sit", id, "entry", e.EntryID, "err", err)
		}
	}
	writeJSON(w, http.StatusCreated, sit)
}

func (h *Handler) getSituation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sit, err := h.Store.GetSituation(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	entries, err := h.Store.ListSituationEntries(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"situation": sit,
		"entries":   entries,
	})
}

func (h *Handler) listSituations(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	sits, err := h.Store.ListSituations(httpCtx(r), projectID, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"situations": sits})
}

type linkEntryRequest struct {
	EntryID   string  `json:"entry_id"`
	Relevance float64 `json:"relevance,omitempty"`
	Notes     string  `json:"notes,omitempty"`
}

func (h *Handler) addSituationEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req linkEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.EntryID == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "entry_id is required", nil)
		return
	}
	if err := h.Store.LinkEntryToSituation(httpCtx(r), id, req.EntryID, req.Relevance, req.Notes); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) removeSituationEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	entryID := chi.URLParam(r, "entryID")
	if err := h.Store.UnlinkEntryFromSituation(httpCtx(r), id, entryID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteSituation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Store.DeleteSituation(httpCtx(r), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ============================================================
// incident clusters
// ============================================================

type clusterRequest struct {
	ID        string `json:"id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	Title     string `json:"title"`
	Summary   string `json:"summary,omitempty"`
	Metadata  string `json:"metadata,omitempty"`
}

func (h *Handler) createCluster(w http.ResponseWriter, r *http.Request) {
	var req clusterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "title is required", nil)
		return
	}
	c := &store.IncidentCluster{
		ID:        req.ID,
		ProjectID: req.ProjectID,
		Title:     req.Title,
		Summary:   req.Summary,
		Metadata:  req.Metadata,
	}
	id, err := h.Store.CreateCluster(httpCtx(r), c)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	c.ID = id
	writeJSON(w, http.StatusCreated, c)
}

func (h *Handler) listClusters(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	cls, err := h.Store.ListClusters(httpCtx(r), q.Get("project_id"), q.Get("status"), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clusters": cls})
}

func (h *Handler) getCluster(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := h.Store.GetCluster(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	members, err := h.Store.ListClusterMembers(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cluster": c,
		"members": members,
	})
}

type clusterMemberRequest struct {
	EntryID    string  `json:"entry_id"`
	Similarity float64 `json:"similarity,omitempty"`
}

func (h *Handler) addClusterMember(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req clusterMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.EntryID == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "entry_id is required", nil)
		return
	}
	if err := h.Store.AddClusterMember(httpCtx(r), id, req.EntryID, req.Similarity, "api"); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) removeClusterMember(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	entryID := chi.URLParam(r, "entryID")
	if err := h.Store.RemoveClusterMember(httpCtx(r), id, entryID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type clusterPromoteRequest struct {
	EntryID string `json:"entry_id"`
}

func (h *Handler) promoteCluster(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req clusterPromoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.EntryID == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields,
			"entry_id is required", nil)
		return
	}
	if err := h.Store.PromoteCluster(httpCtx(r), id, req.EntryID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) dismissCluster(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Store.DismissCluster(httpCtx(r), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rebuildClustersRequest triggers a synchronous clustering pass. The
// background goroutine in the server runs at its own cadence; this handler
// is the admin escape hatch.
type rebuildClustersRequest struct {
	ProjectID  string  `json:"project_id,omitempty"`
	Threshold  float64 `json:"threshold,omitempty"`
	MinMembers int     `json:"min_members,omitempty"`
}

func (h *Handler) rebuildClusters(w http.ResponseWriter, r *http.Request) {
	var req rebuildClustersRequest
	// Allow empty body — admin commonly invokes with defaults.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
			return
		}
	}
	created, added, err := h.Store.BuildIncidentClusters(httpCtx(r),
		req.ProjectID, req.Threshold, req.MinMembers)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"clusters_created": created,
		"members_added":    added,
		"ran_at":           time.Now().UTC().Format(time.RFC3339),
	})
}

