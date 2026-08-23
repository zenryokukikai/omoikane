package dashboard

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// ----------------------------------------------------------------------
// Account-facing pages: /login, the agent-claim landing (/claim/{code}),
// scout watch-topics (/directives) and the viewer's /bookmarks.
// ----------------------------------------------------------------------

// ----------------------------------------------------------------------
// Phase A — login page (no auth required)
// ----------------------------------------------------------------------

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	pc := h.renderCtx(r)
	pc.Title = "omoikane — sign in"
	pc.GoogleEnabled = h.GoogleEnabled
	if next := r.URL.Query().Get("next"); next != "" && strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") {
		pc.Next = next
	}
	if e := r.URL.Query().Get("error"); e != "" {
		pc.LoginError = e
	}
	h.render(w, "login", pc)
}

// claimPage renders the "do you want to claim this agent?" view. The
// page itself is unauthenticated so the human sees what they're being
// asked to adopt; the actual claim is performed by a JS-less form post
// to /v1/agents/claim/{code}, which requires the session cookie.
func (h *Handler) claimPage(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	c, err := h.Store.GetClaimByCode(r.Context(), code)
	pc := h.renderCtx(r)
	pc.Title = "omoikane — claim agent"
	pc.ClaimCode = code
	if err != nil {
		pc.ClaimError = "claim code not found or expired"
		h.render(w, "claim", pc)
		return
	}
	pc.ClaimAgent = c.AgentUser
	pc.ClaimExpiresAt = &c.ExpiresAt
	pc.ClaimedAt = c.ClaimedAt
	if c.ClaimedAt != nil {
		// We don't know the current viewer's user_id without an auth
		// check, but the API endpoint enforces the "different human"
		// guard separately. For display purposes we just flag it.
		pc.ClaimedByMe = false
	}
	h.render(w, "claim", pc)
}

// directivesPage manages operator watch-topics for the scout (issue
// #31) — visible to everyone (the team's shared attention list).
func (h *Handler) directivesPage(w http.ResponseWriter, r *http.Request) {
	pc := h.renderCtx(r)
	pc.Title = "omoikane — 巡回指示"
	ds, err := h.Store.ListDirectives(r.Context(), "scout", false)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc.Directives = ds
	h.render(w, "directives", pc)
}

// bookmarksPage lists the signed-in user's starred entries.
func (h *Handler) bookmarksPage(w http.ResponseWriter, r *http.Request) {
	pc := h.renderCtx(r)
	pc.Title = "omoikane — ブックマーク"
	if pc.Me != nil {
		bms, err := h.Store.ListBookmarks(r.Context(), pc.Me.ID, 200)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		pc.Bookmarks = bms
	}
	h.render(w, "bookmarks", pc)
}
