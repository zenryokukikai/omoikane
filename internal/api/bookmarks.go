package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/auth"
)

// Bookmarks — a human reading aid: star entries, list your shortlist.
// Identity comes from the token/session; there is no way to see or
// touch another user's bookmarks.

// PUT /v1/entries/{id}/bookmark
func (h *Handler) addBookmark(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken, "token required", nil)
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.Store.AddBookmark(httpCtx(r), tok.UserID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entry_id": id, "bookmarked": true})
}

// DELETE /v1/entries/{id}/bookmark
func (h *Handler) removeBookmark(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken, "token required", nil)
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.Store.RemoveBookmark(httpCtx(r), tok.UserID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entry_id": id, "bookmarked": false})
}

// GET /v1/me/bookmarks
func (h *Handler) listMyBookmarks(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken, "token required", nil)
		return
	}
	out, err := h.Store.ListBookmarks(httpCtx(r), tok.UserID, 200)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bookmarks": out, "total": len(out)})
}
