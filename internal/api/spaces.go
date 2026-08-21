package api

// Request-visibility resolution (issue #60, Phase 1 slice 2).
//
// This is the ONE place a token turns into a visible-space list. Every
// authenticated surface — the /v1 API here and the dashboard in slice 5
// — must go through ResolveVisibleSpaces; nothing may consult
// users.role or re-derive visibility on its own (design v2: "admin の
// 視界は1契約に統一").

import (
	"context"
	"net/http"

	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// ResolveVisibleSpaces maps an authenticated token to the caller's
// visible-space list:
//
//   - a token with the "admin" scope sees every space (nil = unrestricted)
//   - a user-bound token sees store.VisibleSpaces(user)
//   - a user-less token without admin (bootstrap/CLI) sees only
//     'internal' (fail-closed)
//   - no token at all sees nothing (fail-closed; cannot happen behind
//     the auth middleware)
//
// The result feeds store.WithVisibleSpaces — tokens can never widen a
// user's view, only the admin scope can lift the restriction.
func ResolveVisibleSpaces(ctx context.Context, s *store.Store, tok *store.APIToken) ([]string, error) {
	if tok == nil {
		return []string{}, nil
	}
	if store.HasScope(tok.Scopes, "admin") {
		return nil, nil
	}
	if tok.UserID == "" {
		return []string{store.SpaceInternal}, nil
	}
	spaces, err := s.VisibleSpaces(ctx, tok.UserID)
	if err != nil {
		return nil, err
	}
	if spaces == nil {
		spaces = []string{} // non-nil: an empty view must stay fail-closed
	}
	return spaces, nil
}

// withVisibleSpaces resolves the request's visibility once and installs
// it on the context for every store call downstream. Mounted directly
// after the auth middleware on all authenticated route groups.
func (h *Handler) withVisibleSpaces(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spaces, err := ResolveVisibleSpaces(r.Context(), h.Store, auth.FromContext(r.Context()))
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeInternal,
				"space visibility resolution failed", nil)
			return
		}
		next.ServeHTTP(w, r.WithContext(store.WithVisibleSpaces(r.Context(), spaces)))
	})
}
