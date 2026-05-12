package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/auth"
	"github.com/kojira/omoikane/internal/store"
)

// ----------------------------------------------------------------------
// /v1/users — public profile lookup.
//
// Motivation: in shared spaces (chat threads, audit-log views, "who
// just edited this entry") an authenticated participant needs to know
// who they're talking to / reading after. /v1/auth/me only describes
// self; this endpoint describes anyone in the directory.
//
// Privacy model: we deliberately strip identifiers that aren't useful
// to "what kind of entity is this" — specifically email, google_sub,
// email_verified_at. Those carry account-recovery risk if scraped and
// answer the wrong question anyway. We keep name, role, description
// (the agent's self-introduction), parent_user_id + parent_name (so
// you can tell "agent X belongs to human Y") and timestamps.
//
// Auth: any read-scoped token can call. Agents and humans alike. We
// don't restrict by parent because the whole point is cross-org
// discovery — if it's in the same omoikane instance, you can see its
// profile.
// ----------------------------------------------------------------------

// PublicProfile is the safe-to-expose subset of a User.
//
// Compared with store.User this drops Email, GoogleSub,
// EmailVerifiedAt. We add ParentName so callers don't need a second
// round-trip to render "agent X (owned by human Y)" — the very first
// question someone asks when they encounter an unfamiliar agent in
// chat.
type PublicProfile struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Role         string     `json:"role"`
	Description  string     `json:"description,omitempty"`
	AvatarURL    string     `json:"avatar_url,omitempty"`
	ParentUserID string     `json:"parent_user_id,omitempty"`
	ParentName   string     `json:"parent_name,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

// toPublicProfile redacts a *store.User into the wire format.
// parentName is looked up by the handler when ParentUserID is set;
// passing "" is fine (the field is omitempty).
func toPublicProfile(u *store.User, parentName string) PublicProfile {
	p := PublicProfile{
		ID:           u.ID,
		Name:         u.Name,
		Role:         u.Role,
		Description:  u.Description,
		AvatarURL:    u.AvatarURL,
		ParentUserID: u.ParentUserID,
		ParentName:   parentName,
		CreatedAt:    u.CreatedAt,
		LastLoginAt:  u.LastLoginAt,
	}
	return p
}

// getUser returns the public profile for a single user by ID. 404 if
// unknown. No authorization check beyond "you're authenticated" — the
// gate is upstream in api.go (RequireScope("read")).
func (h *Handler) getUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "id required", nil)
		return
	}
	u, err := h.Store.GetUser(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var parentName string
	if u.ParentUserID != "" {
		if p, perr := h.Store.GetUser(httpCtx(r), u.ParentUserID); perr == nil {
			parentName = p.Name
		}
	}
	writeJSON(w, http.StatusOK, toPublicProfile(u, parentName))
}

// listUsers returns the directory. Optional ?role=agent|member|admin
// filter and ?limit=N (default 200, max 500).
//
// We resolve parent names in a single pass — for a directory of ~100s
// of users this is fine; if the user table ever balloons we'd swap to
// a join or a parent-name cache, but that's not on the horizon.
func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	role := r.URL.Query().Get("role")
	switch role {
	case "", "admin", "member", "agent":
		// ok
	default:
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"role must be admin|member|agent (or omitted)", nil)
		return
	}
	limit := 200
	if l := r.URL.Query().Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, CodeBadRequest,
				"limit must be a positive integer", nil)
			return
		}
		if n > 500 {
			n = 500
		}
		limit = n
	}
	users, err := h.Store.ListUsers(httpCtx(r), role, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Build a small id→name lookup for parent resolution. Most parents
	// are themselves in the returned slice (humans whose agents are
	// also being listed); the rest fall back to a per-user GetUser.
	byID := make(map[string]string, len(users))
	for _, u := range users {
		byID[u.ID] = u.Name
	}
	out := make([]PublicProfile, 0, len(users))
	for _, u := range users {
		pn := ""
		if u.ParentUserID != "" {
			if n, ok := byID[u.ParentUserID]; ok {
				pn = n
			} else if p, perr := h.Store.GetUser(httpCtx(r), u.ParentUserID); perr == nil {
				pn = p.Name
				byID[u.ParentUserID] = p.Name
			}
		}
		out = append(out, toPublicProfile(u, pn))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

// userPatchRequest is the body of PATCH /v1/users/me. Pointer fields
// distinguish "field not present in body" (nil) from "field set to
// empty" (non-nil pointer to ""). The latter is the documented way to
// clear an avatar / description.
type userPatchRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
}

// patchMe is the self-editable profile endpoint. Agents use it to
// revise their self-introduction as they learn what their actual
// niche is; humans use it to set a display name distinct from their
// google account name, or to swap an avatar URL.
//
// Intentionally NOT generalizable to patching anyone — we look up
// "me" from the auth token and ignore any id in the URL. If an admin
// needs to fix another user's profile, that's a separate endpoint
// (not yet built — the immediate need is self-editing).
func (h *Handler) patchMe(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken,
			"no authenticated user", nil)
		return
	}
	var req userPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	u, err := h.Store.UpdateUserProfile(httpCtx(r), tok.UserID, store.UserProfilePatch{
		Name:        req.Name,
		Description: req.Description,
		AvatarURL:   req.AvatarURL,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Return the public-profile shape so callers see exactly what other
	// viewers would see — no surprises about what's exposed vs hidden.
	var parentName string
	if u.ParentUserID != "" {
		if p, perr := h.Store.GetUser(httpCtx(r), u.ParentUserID); perr == nil {
			parentName = p.Name
		}
	}
	writeJSON(w, http.StatusOK, toPublicProfile(u, parentName))
}
