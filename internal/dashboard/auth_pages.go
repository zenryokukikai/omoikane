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
	next := safeNext(r.URL.Query().Get("next"))
	// Already signed in? Don't render a login form — bounce to the
	// intended destination (default "/"). This unbreaks the mobile
	// handoff (#129): an in-app browser (e.g. Slack) rewrites the URL to
	// /login?next=… , the user reopens it in a browser that IS signed in,
	// and lands on their entry instead of a pointless login screen.
	//
	// The bounce is unconditional for a signed-in visitor, including when
	// ?error=… is present: the only producer of that param is a failed
	// OAuth attempt for a visitor who is NOT yet authenticated, so an
	// authenticated visitor has no error here worth stopping to read.
	if pc.Me != nil {
		dest := next
		if dest == "" {
			dest = "/"
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	pc.Title = "omoikane — sign in"
	pc.GoogleEnabled = h.GoogleEnabled
	pc.Next = next
	if e := r.URL.Query().Get("error"); e != "" {
		pc.LoginError = e
	}
	h.render(w, "login", pc)
}

// safeNext returns raw when it is a safe same-origin redirect target
// (starts with "/" but not "//", which would be a protocol-relative URL
// to another host), otherwise "". This is the single open-redirect guard
// for the login page: it gates both the ?next echoed into the form and
// the post-sign-in bounce target, so the contract lives in one place.
func safeNext(raw string) string {
	// The backslash rejection is load-bearing: browsers normalise "\" to
	// "/" during URL parsing, so a Location of "/\evil.example" becomes
	// the scheme-relative "//evil.example" — an open redirect that the
	// "//" prefix check alone does not catch. Go's url parsing does NOT
	// normalise backslashes, which is exactly why this must be explicit.
	if raw != "" && strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") &&
		!strings.ContainsRune(raw, '\\') &&
		strings.IndexFunc(raw, func(r rune) bool { return r < 0x20 }) < 0 {
		return raw
	}
	return ""
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
