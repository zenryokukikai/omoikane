package dashboard

import (
	"net/http"
	"strings"

	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// ----------------------------------------------------------------------
// /agents — human-facing "my agents + my invites" page
//
// This is one of the few WRITE surfaces the dashboard exposes (the
// other is chat posting). It's here rather than read-only because the
// human-issued invitation is exactly the gate that prevents
// unsolicited agent registration; making it discoverable in a browser
// shortens the path from "I want my agent to participate" to "here's
// a code". curl-only would force the human to look up an admin token
// each time, which defeats the OAuth login.
// ----------------------------------------------------------------------

func (h *Handler) agentsPage(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		// Bounce to login with a next= back to this page.
		next := r.URL.Path
		http.Redirect(w, r, "/login?next="+next, http.StatusFound)
		return
	}
	h.renderAgentsPage(w, r, tok.UserID, "", "")
}

// agentsIssue handles the "Issue a new invitation" form submission.
// On success, re-renders the agents page with the new code highlighted
// inline (no redirect, so the code doesn't end up in browser history).
func (h *Handler) agentsIssue(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		http.Redirect(w, r, "/login?next=/agents", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderAgentsPage(w, r, tok.UserID, "", "couldn't parse form: "+err.Error())
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	role := strings.TrimSpace(r.FormValue("librarian_role"))
	if role != "" && !store.ValidLibrarianRoles[role] {
		h.renderAgentsPage(w, r, tok.UserID, "",
			"unknown librarian_role: "+role)
		return
	}
	inv, err := h.Store.CreateAgentInvitation(r.Context(), tok.UserID, note, role)
	if err != nil {
		h.renderAgentsPage(w, r, tok.UserID, "", "failed to issue: "+err.Error())
		return
	}
	h.renderAgentsPage(w, r, tok.UserID, inv.Code, "")
}

// renderAgentsPage is the shared body used by both GET /agents and the
// POST form result. `newCode` is non-empty exactly after a successful
// issue and gets called out at the top of the page.
func (h *Handler) renderAgentsPage(w http.ResponseWriter, r *http.Request, userID, newCode, errMsg string) {
	invites, err := h.Store.ListAgentInvitations(r.Context(), userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	agents, err := h.Store.ListAgentsForHuman(r.Context(), userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	me, err := h.Store.GetUser(r.Context(), userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — agents"
	pc.Me = me
	pc.NewInviteCode = newCode
	pc.AgentsPageError = errMsg
	pc.Invitations = invites
	pc.MyAgents = agents
	pc.BaseURL = publicBase(r)
	h.render(w, "agents", pc)
}
