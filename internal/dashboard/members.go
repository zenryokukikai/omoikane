package dashboard

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// ----------------------------------------------------------------------
// /members — admin-only member management page.
//
// Surfaces the same operations the API exposes (invite issuance, role
// change) so admins can do them in a browser instead of needing curl
// + an admin token. Non-admins viewing /members get a 403-shaped
// banner (we don't 404 to avoid confusion — they should know the
// feature exists, just isn't theirs).
//
// /members/claim/{code} is the PUBLIC landing page the invitee opens.
// It shows what they're claiming (role, inviter, expires) and offers
// a "Sign in with Google" CTA. The actual redemption happens in the
// OAuth callback by email match — this page is just a courtesy
// preview.
// ----------------------------------------------------------------------

func (h *Handler) membersPage(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusFound)
		return
	}
	me, err := h.Store.GetUser(r.Context(), tok.UserID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — members"
	pc.Me = me
	pc.BaseURL = publicBase(r)

	if me.Role != "admin" {
		pc.MembersPageError = "Only admins can view the member management page. " +
			"You are signed in as " + me.Role + "."
		h.render(w, "members", pc)
		return
	}

	// All humans — for the directory table.
	users, err := h.Store.ListUsers(r.Context(), "", 500)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	humans := make([]*store.User, 0, len(users))
	for _, u := range users {
		if u.Role == "agent" {
			continue // /agents shows agents; /members is humans
		}
		// Don't redact email here — admins on /members legitimately
		// need to see emails (to know who's invited as whom). The
		// privacy-protected surface is /v1/users + /u/{id}, not the
		// admin-only management view.
		humans = append(humans, u)
	}
	pc.MembersList = humans

	invs, err := h.Store.ListMemberInvitations(r.Context(), "")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc.MemberInvitations = invs
	// ?new=<code> is set by the POST handler so we can highlight the
	// freshly issued invite at the top of the page. Validate the code
	// against the just-fetched list so a hand-crafted URL with a bogus
	// code is silently ignored.
	if newCode := r.URL.Query().Get("new"); newCode != "" {
		for _, inv := range invs {
			if inv.Code == newCode {
				pc.NewMemberCode = newCode
				break
			}
		}
	}
	h.render(w, "members", pc)
}

// membersInvite handles POST /members/invite (admin form submission).
// On success re-renders the page with the new invitation highlighted.
func (h *Handler) membersInvite(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		http.Redirect(w, r, "/login?next=/members", http.StatusFound)
		return
	}
	me, err := h.Store.GetUser(r.Context(), tok.UserID)
	if err != nil || me.Role != "admin" {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	role := strings.TrimSpace(r.FormValue("role"))
	note := strings.TrimSpace(r.FormValue("note"))
	if email == "" {
		http.Error(w, "email required", http.StatusBadRequest)
		return
	}
	if role == "" {
		role = "member"
	}
	inv, err := h.Store.CreateMemberInvitation(r.Context(), tok.UserID, email, role, note)
	if err != nil {
		http.Error(w, "couldn't issue: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Bounce back to /members with the new code highlighted via query
	// param. We avoid putting the code in a path segment so it doesn't
	// leak through browser history any more than necessary.
	dest := "/members?new=" + inv.Code
	if t := r.URL.Query().Get("token"); t != "" {
		dest += "&token=" + t
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// membersRoleChange handles POST /members/{id}/role — admin form for
// promote/demote. Body: role=admin|member.
func (h *Handler) membersRoleChange(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		http.Redirect(w, r, "/login?next=/members", http.StatusFound)
		return
	}
	me, err := h.Store.GetUser(r.Context(), tok.UserID)
	if err != nil || me.Role != "admin" {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	targetID := chi.URLParam(r, "id")
	newRole := strings.TrimSpace(r.FormValue("role"))
	if _, err := h.Store.UpdateUserRole(r.Context(), targetID, newRole); err != nil {
		http.Error(w, "couldn't change role: "+err.Error(), http.StatusBadRequest)
		return
	}
	dest := "/members"
	if t := r.URL.Query().Get("token"); t != "" {
		dest += "?token=" + t
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// claimPage handles GET /members/claim/{code} — the public landing
// the invitee opens. Shows the role they'd get, who invited them,
// when it expires, and a Sign-In CTA. No auth required.
func (h *Handler) memberClaimPage(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	inv, err := h.Store.GetMemberInvitation(r.Context(), code)
	pc := h.renderCtx(r)
	pc.Title = "omoikane — invitation"
	if err != nil {
		pc.MembersPageError = "invitation not found"
		w.WriteHeader(http.StatusNotFound)
		h.render(w, "member_claim", pc)
		return
	}
	pc.ClaimInvitation = inv
	// Inviter's name is nice to surface — saves the invitee from
	// asking "who's me?"
	if inviter, err := h.Store.GetUser(r.Context(), inv.InviterUserID); err == nil {
		pc.ClaimInviter = inviter
	}
	h.render(w, "member_claim", pc)
}

