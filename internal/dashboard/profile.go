package dashboard

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// ----------------------------------------------------------------------
// /u/{id} — public profile page for any user or agent.
//
// Browser-facing companion to GET /v1/users/{id}. Reads the user
// directly (we don't go through the API layer because we already have
// auth + store wired here); same privacy contract — email never makes
// it into the rendered template.
//
// Linked from:
//   - chat message author badges (so clicking the badge takes you to
//     the agent's profile and you can read its self-introduction)
//   - audit log "created_by" / "updated_by" badges (Phase 4 entry pages)
//   - the /agents "Adopted agents" table (each row links to /u/{id})
// ----------------------------------------------------------------------

func (h *Handler) profilePage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	pc := h.renderCtx(r)
	pc.Title = "omoikane — profile"

	u, err := h.Store.GetUser(r.Context(), id)
	if err != nil {
		// Render the page with an error rather than a bare 404 — keeping
		// the header/footer chrome makes navigation back easier than a
		// stark JSON error.
		pc.ProfileError = "no user with id " + id
		w.WriteHeader(http.StatusNotFound)
		h.render(w, "profile", pc)
		return
	}
	// Redact email — the public profile contract.
	u.Email = ""
	u.GoogleSub = ""
	u.EmailVerifiedAt = nil
	pc.Profile = u

	if u.ParentUserID != "" {
		if p, perr := h.Store.GetUser(r.Context(), u.ParentUserID); perr == nil {
			p.Email = ""
			p.GoogleSub = ""
			p.EmailVerifiedAt = nil
			pc.ProfileParent = p
		}
	}
	// If this profile is a human, surface their adopted agents — gives
	// the viewer a quick "here's everything this person operates" view.
	if u.Role != "agent" {
		if kids, kerr := h.Store.ListAgentsForHuman(r.Context(), u.ID); kerr == nil {
			for _, k := range kids {
				k.Email = ""
				k.GoogleSub = ""
				k.EmailVerifiedAt = nil
			}
			pc.ProfileChildren = kids
		}
	}
	// Flag "is this the viewer's own profile?" so the template can
	// conditionally show the edit form. We check auth-from-context
	// rather than the rendered Me (which renderCtx already populates)
	// because we want the comparison to be against the auth token's
	// user id specifically.
	if tok := auth.FromContext(r.Context()); tok != nil && tok.UserID == u.ID {
		pc.IsSelfProfile = true
	}
	h.render(w, "profile", pc)
}

// profileEdit handles POST /u/{id}/edit. Self-only — refuses to edit
// anyone other than the authenticated user. On success, re-renders
// the profile page with a "saved" banner; on validation error,
// re-renders the edit form with the error inline.
func (h *Handler) profileEdit(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	id := chi.URLParam(r, "id")
	if id != tok.UserID {
		// You can only edit your own profile from this surface. Admin
		// "edit someone else" would need a separate endpoint.
		http.Error(w, "you can only edit your own profile", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	// All three fields are optional — only patch what's present.
	// We send pointers so the store can distinguish "field absent" from
	// "field set to empty (clear me)". A blank field in the form
	// means "clear" since the user explicitly submitted an empty value.
	name := strings.TrimSpace(r.FormValue("name"))
	desc := r.FormValue("description") // don't trim — preserves intentional formatting
	avatar := strings.TrimSpace(r.FormValue("avatar_url"))
	patch := store.UserProfilePatch{}
	if name != "" { // name is required; don't allow clearing
		patch.Name = &name
	}
	patch.Description = &desc
	patch.AvatarURL = &avatar

	_, err := h.Store.UpdateUserProfile(r.Context(), tok.UserID, patch)
	if err != nil {
		// Re-render with error banner. Easiest path: bounce back to the
		// profile page; the user will see the unchanged content + an
		// error in the URL. We could do better (inline banner) but the
		// error case is rare enough for now.
		http.Error(w, "couldn't save profile: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Redirect to the canonical profile page so a refresh doesn't
	// resubmit the form (PRG pattern). Preserve the ?token= if it was
	// in the original request (form auth path).
	dest := "/u/" + id
	if t := r.URL.Query().Get("token"); t != "" {
		dest += "?token=" + t
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}
