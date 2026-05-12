package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/store"
)

// ============================================================
// /v1/browse + /v1/index (Phase 4)
// ============================================================

type hierarchyNodeRequest struct {
	ID          string `json:"id,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	ParentID    string `json:"parent_id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	SortOrder   int    `json:"sort_order,omitempty"`
	Metadata    string `json:"metadata,omitempty"`
}

func (h *Handler) createHierarchyNode(w http.ResponseWriter, r *http.Request) {
	var req hierarchyNodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "name is required", nil)
		return
	}
	n := &store.HierarchyNode{
		ID:          req.ID,
		ProjectID:   req.ProjectID,
		ParentID:    req.ParentID,
		Name:        req.Name,
		Description: req.Description,
		SortOrder:   req.SortOrder,
		Metadata:    req.Metadata,
	}
	id, err := h.Store.CreateHierarchyNode(httpCtx(r), n)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	n.ID = id
	writeJSON(w, http.StatusCreated, n)
}

// browseRoots returns the top-level (parent IS NULL) nodes for /v1/browse.
func (h *Handler) browseRoots(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	nodes, err := h.Store.ListHierarchyNodes(httpCtx(r), projectID, "")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

// browseNode returns one node + its direct children.
func (h *Handler) browseNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	node, err := h.Store.GetHierarchyNode(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	children, err := h.Store.ListHierarchyNodes(httpCtx(r), node.ProjectID, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	summary, _ := h.Store.GetDerivedSummary(httpCtx(r), "hierarchy_node", id)
	writeJSON(w, http.StatusOK, map[string]any{
		"node":     node,
		"children": children,
		"summary":  summary,
	})
}

func (h *Handler) browseNodeEntries(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := h.Store.ListEntriesAtNode(httpCtx(r), id, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

type attachEntryRequest struct {
	EntryID string  `json:"entry_id"`
	Weight  float64 `json:"weight,omitempty"`
}

func (h *Handler) attachEntryToNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req attachEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.EntryID == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "entry_id is required", nil)
		return
	}
	if err := h.Store.AttachEntryToNode(httpCtx(r), id, req.EntryID, req.Weight, "api"); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) detachEntryFromNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	entryID := chi.URLParam(r, "entryID")
	if err := h.Store.DetachEntryFromNode(httpCtx(r), id, entryID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteHierarchyNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Store.DeleteHierarchyNode(httpCtx(r), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// /v1/index?group_by=tag|hierarchy|recent
func (h *Handler) indexPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	groupBy := q.Get("group_by")
	if groupBy == "" {
		groupBy = "tag"
	}
	projectID := q.Get("project_id")
	limit, _ := strconv.Atoi(q.Get("limit"))
	var (
		buckets []*store.IndexBucket
		err     error
	)
	switch groupBy {
	case "tag":
		buckets, err = h.Store.IndexByTag(httpCtx(r), projectID, limit)
	case "recent":
		buckets, err = h.Store.IndexByRecent(httpCtx(r), projectID, limit)
	case "hierarchy":
		buckets, err = h.Store.IndexByHierarchy(httpCtx(r), projectID)
	default:
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"group_by must be one of tag|recent|hierarchy",
			map[string]any{"got": groupBy})
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"group_by": groupBy,
		"buckets":  buckets,
	})
}

// ============================================================
// /v1/reflect (Phase 4)
// ============================================================

type reflectRequest struct {
	EntryIDs []string `json:"entry_ids"`
	Prompt   string   `json:"prompt,omitempty"`
}

// reflect runs a cross-entry summarisation. Without an LLM configured it
// returns a heuristic stub: title + bullet of each entry's symptom or
// resolution. With an LLM available it would delegate; for Phase 4 we
// surface a 501 if the caller explicitly asks for an LLM-quality answer
// they can't get.
func (h *Handler) reflect(w http.ResponseWriter, r *http.Request) {
	var req reflectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if len(req.EntryIDs) == 0 {
		writeError(w, http.StatusBadRequest, CodeMissingFields,
			"entry_ids is required", nil)
		return
	}
	out := map[string]any{
		"prompt":   req.Prompt,
		"entries":  []map[string]string{},
		"summary":  "",
		"engine":   "heuristic",
	}
	bullets := make([]string, 0, len(req.EntryIDs))
	entries := make([]map[string]string, 0, len(req.EntryIDs))
	for _, id := range req.EntryIDs {
		e, err := h.Store.GetEntry(httpCtx(r), id)
		if err != nil {
			continue
		}
		entries = append(entries, map[string]string{
			"id": e.ID, "title": e.Title, "type": e.Type,
		})
		gist := e.Resolution
		if gist == "" {
			gist = e.Symptom
		}
		if gist == "" {
			gist = e.Title
		}
		bullets = append(bullets, "- ["+e.ID+"] "+e.Title+": "+gist)
	}
	out["entries"] = entries
	if len(bullets) > 0 {
		out["summary"] = "Cross-entry summary:\n" + joinLines(bullets)
	}
	writeJSON(w, http.StatusOK, out)
}

func joinLines(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	out := ss[0]
	for _, s := range ss[1:] {
		out += "\n" + s
	}
	return out
}
