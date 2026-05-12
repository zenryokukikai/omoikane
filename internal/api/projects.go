package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/store"
)

type projectCreateReq struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Metadata    any    `json:"metadata,omitempty"`
}

func (h *Handler) createProject(w http.ResponseWriter, r *http.Request) {
	var req projectCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.Name = strings.TrimSpace(req.Name)
	if req.ID == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "id and name are required",
			map[string]any{"fields": []string{"id", "name"}})
		return
	}
	metaJSON := marshalJSONField(req.Metadata)
	p := store.Project{
		ID:          req.ID,
		Name:        req.Name,
		Description: req.Description,
		Metadata:    metaJSON,
	}
	if err := h.Store.CreateProject(httpCtx(r), &p); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, CodeAlreadyExists,
				"Project ID already in use", map[string]string{"id": req.ID})
			return
		}
		writeStoreError(w, err)
		return
	}
	// store.CreateProject populates p.CreatedAt so we don't need a re-fetch.
	writeJSON(w, http.StatusCreated, &p)
}

func (h *Handler) listProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := h.Store.ListProjects(httpCtx(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": ps})
}

func (h *Handler) getProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := h.Store.GetProject(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// marshalJSONField re-serialises a JSON-decoded value back to compact JSON
// text. Since the caller always supplies a value produced by json.Decode,
// json.Marshal cannot fail — we therefore swallow the (unreachable) error
// rather than propagate it to callers, eliminating dead defensive branches.
func marshalJSONField(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return ""
	}
	return string(b)
}
