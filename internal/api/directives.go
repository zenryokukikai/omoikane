package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// Operator directives (issue #31): humans register "watch this topic"
// for a librarian role from the UI; the role's agent fetches them each
// patrol and EXPANDS its collection accordingly — directives add to
// the standing criteria, never replace them.

// GET /v1/librarian/directives?role=scout&active=1
func (h *Handler) listDirectives(w http.ResponseWriter, r *http.Request) {
	ds, err := h.Store.ListDirectives(httpCtx(r),
		r.URL.Query().Get("role"), r.URL.Query().Get("active") == "1")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"directives": ds, "total": len(ds)})
}

// POST /v1/librarian/directives {role, text}
func (h *Handler) createDirective(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken, "token required", nil)
		return
	}
	var req struct {
		Role string `json:"role"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	d, err := h.Store.CreateDirective(httpCtx(r), req.Role, req.Text, tok.UserID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Push to SSE/webhook listeners — an external agent runtime can
	// react to a fresh watch-topic without waiting for its next patrol.
	if h.Events != nil {
		h.Events.Publish(Event{Type: "directive.created", Data: d})
	}
	writeJSON(w, http.StatusCreated, d)
}

// PATCH /v1/librarian/directives/{id} {active}
func (h *Handler) patchDirective(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Active *bool `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Active == nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, "active (bool) required", nil)
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.Store.SetDirectiveActive(httpCtx(r), id, *req.Active); err != nil {
		writeStoreError(w, err)
		return
	}
	d, err := h.Store.GetDirective(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// DELETE /v1/librarian/directives/{id} — creator or admin.
func (h *Handler) deleteDirective(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken, "token required", nil)
		return
	}
	id := chi.URLParam(r, "id")
	d, err := h.Store.GetDirective(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if d.CreatedBy != tok.UserID && !store.HasScope(tok.Scopes, "admin") {
		writeError(w, http.StatusForbidden, CodeForbidden, "only the creator or an admin can delete a directive", nil)
		return
	}
	if err := h.Store.DeleteDirective(httpCtx(r), id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}
