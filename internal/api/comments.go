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

// reviewRequestHeader stamps X-Review-Requests: <n> on authenticated
// responses when the caller has open @mention review requests, so an agent
// learns it has reviews waiting on its very next call — no polling. Mirrors
// the X-Skill-Version pull pattern. Degrades silently (count failure → no
// header); it must never block the underlying request.
func (h *Handler) reviewRequestHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok := auth.FromContext(r.Context()); tok != nil && tok.UserID != "" {
			if n, err := h.Store.CountReviewRequests(r.Context(), tok.UserID); err == nil && n > 0 {
				w.Header().Set("X-Review-Requests", strconv.Itoa(n))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Entry comments — review / discussion threads anchored to one entry,
// written by humans AND agents. See design.md §23.21.
//
// Authorship comes from the bearer token (the caller's users.id); clients
// cannot set it. Posting needs `write` scope (every human member and every
// agent token has it); reading needs `read`.

type createCommentReq struct {
	Body     string   `json:"body"`
	ReplyTo  string   `json:"reply_to,omitempty"`
	Mentions []string `json:"mentions,omitempty"` // user ids or librarian roles to notify
}

type updateCommentReq struct {
	Body     *string `json:"body,omitempty"`
	Resolved *bool   `json:"resolved,omitempty"`
}

// POST /entries/{id}/comments
func (h *Handler) createEntryComment(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken, "a user- or agent-bound token is required to comment", nil)
		return
	}
	entryID := chi.URLParam(r, "id")
	// Reject comments on unknown entries up front (404, not a dangling row).
	entry, err := h.Store.GetEntry(httpCtx(r), entryID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req createCommentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "body required", nil)
		return
	}
	c, cerr := h.Store.CreateComment(httpCtx(r), entryID, tok.UserID,
		req.Body, strings.TrimSpace(req.ReplyTo), req.Mentions)
	if err := cerr; err != nil {
		if err == store.ErrNotFound {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "reply_to comment not found", nil)
			return
		}
		if strings.Contains(err.Error(), "different entry") {
			writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
			return
		}
		writeStoreError(w, err)
		return
	}
	// Push to SSE listeners (comment responders etc.) with the entry
	// context they filter on — same shape as the recent-comments feed.
	if h.Events != nil {
		// Delivered under the entry's space (slice 4): a comment on a
		// restricted entry reaches only that space's members.
		h.Events.Publish(Event{Type: "comment.created", Data: store.RecentComment{
			Comment:        c,
			EntryTitle:     entry.Title,
			EntryType:      entry.Type,
			EntryCreatedBy: entry.CreatedBy,
		}, SpaceID: entry.SpaceID})
	}
	writeJSON(w, http.StatusCreated, c)
}

// GET /entries/{id}/comments
func (h *Handler) listEntryComments(w http.ResponseWriter, r *http.Request) {
	entryID := chi.URLParam(r, "id")
	if _, err := h.Store.GetEntry(httpCtx(r), entryID); err != nil {
		writeStoreError(w, err)
		return
	}
	comments, err := h.Store.ListComments(httpCtx(r), entryID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entry_id": entryID,
		"comments": comments,
		"total":    len(comments),
	})
}

// PATCH /comments/{cid} — edit body (author only) and/or toggle resolved
// (any writer — resolving a review thread is collaborative).
func (h *Handler) updateComment(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken, "token required", nil)
		return
	}
	cid := chi.URLParam(r, "cid")
	existing, err := h.Store.GetComment(httpCtx(r), cid)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req updateCommentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.Body == nil && req.Resolved == nil {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "nothing to update (body or resolved)", nil)
		return
	}
	// Editing the prose is author-only; the resolved flag is open to any
	// writer so a reviewer can close a thread the author addressed.
	isAuthor := tok.UserID == existing.AuthorUserID
	isAdmin := store.HasScope(tok.Scopes, "admin")
	if req.Body != nil && !isAuthor && !isAdmin {
		writeError(w, http.StatusForbidden, CodeForbidden, "only the author can edit a comment's text", nil)
		return
	}
	updated, err := h.Store.UpdateComment(httpCtx(r), cid, req.Body, req.Resolved)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// DELETE /comments/{cid} — author or admin only.
func (h *Handler) deleteComment(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken, "token required", nil)
		return
	}
	cid := chi.URLParam(r, "cid")
	existing, err := h.Store.GetComment(httpCtx(r), cid)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if tok.UserID != existing.AuthorUserID && !store.HasScope(tok.Scopes, "admin") {
		writeError(w, http.StatusForbidden, CodeForbidden, "only the author or an admin can delete a comment", nil)
		return
	}
	if err := h.Store.DeleteComment(httpCtx(r), cid); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": cid, "deleted": true})
}

// GET /v1/comments/recent — newest comments across ALL entries, optionally
// filtered by ?entry_created_by=<uid> and ?since=<RFC3339>. One call lets a
// responder agent (e.g. scout answering questions on its own findings) see
// every fresh comment on its entries without walking them (§23.21).
func (h *Handler) listRecentComments(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	out, err := h.Store.ListRecentComments(httpCtx(r),
		strings.TrimSpace(r.URL.Query().Get("entry_created_by")),
		strings.TrimSpace(r.URL.Query().Get("since")), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"comments": out,
		"total":    len(out),
	})
}

// GET /v1/me/review-requests — the open comments that @mention the caller
// (by user id or librarian role) and that they didn't write. This is the
// pull side of the X-Review-Requests header notification (§23.21).
func (h *Handler) listMyReviewRequests(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken, "token required", nil)
		return
	}
	reqs, err := h.Store.ListReviewRequests(httpCtx(r), tok.UserID, 50)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"review_requests": reqs,
		"total":           len(reqs),
		"how_to_resolve":  "reply with POST /v1/entries/{entry_id}/comments then PATCH /v1/comments/{id} {\"resolved\":true}",
	})
}
