package dashboard

// Space visibility for the dashboard (issue #60, Phase 1 slice 5).
//
// Every authenticated dashboard request resolves its visible-space list
// through api.ResolveVisibleSpaces — the SAME single contract the /v1
// surface uses (admin scope = unrestricted; user-bound token =
// store.VisibleSpaces; user-less non-admin token = internal only). With
// the visibility installed on the context, the store gatekeepers from
// slices 1–4 (spaceCond / visibleEntryExists / requireVisible*) apply
// to every page automatically; the dashboard never re-derives
// visibility on its own.
//
// Open mode (h.Open, no auth) keeps its unrestricted view: it exists
// only for local single-user development, and there is no principal to
// resolve a view for.

import (
	"net/http"

	"github.com/zenryokukikai/omoikane/internal/api"
	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// withVisibleSpaces mirrors api.(*Handler).withVisibleSpaces for the
// dashboard's HTML surface: resolve once per request, install on the
// context, carry the viewer's user id for owner-scoped predicates
// (talk threads).
func (h *Handler) withVisibleSpaces(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := auth.FromContext(r.Context())
		spaces, err := api.ResolveVisibleSpaces(r.Context(), h.Store, tok)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		ctx := store.WithVisibleSpaces(r.Context(), spaces)
		if tok != nil {
			ctx = store.WithViewerUser(ctx, tok.UserID)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isAdmin reports whether the request's token carries the admin scope —
// the ONE admin contract (design v2). The dashboard must never consult
// users.role for authorisation; session tokens mirror the role into
// scopes at login, and API tokens carry their scopes explicitly.
func isAdmin(r *http.Request) bool {
	tok := auth.FromContext(r.Context())
	return tok != nil && store.HasScope(tok.Scopes, "admin")
}
