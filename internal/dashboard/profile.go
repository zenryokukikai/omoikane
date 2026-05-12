package dashboard

import (
	"net/http"

	"github.com/go-chi/chi/v5"
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
	h.render(w, "profile", pc)
}
