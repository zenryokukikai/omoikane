package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/auth"
)

// ----------------------------------------------------------------------
// Member management — admin-only surface.
//
//   POST   /v1/admin/members/invitations           issue an invite
//   GET    /v1/admin/members/invitations           list invites
//   PATCH  /v1/admin/users/{id}/role               change role
//
// The "claim" half of the invite lives in the OAuth callback (see
// authGoogleCallback): when a new email lands and matches an open
// invitation, it's redeemed there. The dashboard surfaces a
// /members/claim/{code} landing page that lets the invitee read the
// invitation before signing in, but the actual security gate is the
// email match at OAuth callback time, not the code itself.
//
// Why admin-only for issuance: humans on this instance can already
// invite agents (which they parent + are responsible for). Inviting
// other HUMANS is a heavier act — they become peers, not subjects —
// so we restrict it to admin scope. Demote later if it proves too
// restrictive.
// ----------------------------------------------------------------------

type memberInviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role,omitempty"` // 'admin' | 'member' — defaults to member
	Note  string `json:"note,omitempty"`
}

type memberInviteResponse struct {
	Code         string `json:"code"`
	TargetEmail  string `json:"target_email"`
	TargetRole   string `json:"target_role"`
	ExpiresAt    string `json:"expires_at"`
	ClaimURL     string `json:"claim_url"`
	Instructions string `json:"instructions"`
}

func (h *Handler) issueMemberInvite(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken, "sign in to issue invites", nil)
		return
	}
	var req memberInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "email required", nil)
		return
	}
	if req.Role == "" {
		req.Role = "member"
	}
	inv, err := h.Store.CreateMemberInvitation(httpCtx(r), tok.UserID, email, req.Role, req.Note)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	base := h.publicBase(r)
	writeJSON(w, http.StatusCreated, memberInviteResponse{
		Code:        inv.Code,
		TargetEmail: inv.TargetEmail,
		TargetRole:  inv.TargetRole,
		ExpiresAt:   inv.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
		ClaimURL:    base + "/members/claim/" + inv.Code,
		Instructions: "Send the ClaimURL to " + inv.TargetEmail + ". " +
			"They open it, click 'Sign in with Google', and on a successful " +
			"OAuth handshake (with the matching email) the invitation is " +
			"automatically redeemed and they're granted role=" + inv.TargetRole + ".",
	})
}

func (h *Handler) listMemberInvites(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken, "sign in", nil)
		return
	}
	// Admin sees everyone's invites (it's the management surface);
	// non-admin shouldn't reach this handler at all because the route
	// is gated by RequireScope("admin"), but be defensive.
	invs, err := h.Store.ListMemberInvitations(httpCtx(r), "")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitations": invs})
}

type updateRoleRequest struct {
	Role string `json:"role"`
}

// updateUserRole changes a user's role between admin and member.
// Agents are rejected at the store layer. The "last admin" lockout
// guard also lives in the store (UpdateUserRole). This handler is
// mostly a thin shell around the store call.
func (h *Handler) updateUserRole(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "id required", nil)
		return
	}
	var req updateRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	u, err := h.Store.UpdateUserRole(httpCtx(r), id, req.Role)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Return the public profile so admin tools see exactly the
	// post-change state (and so we don't accidentally leak email
	// from a fuller User struct).
	writeJSON(w, http.StatusOK, toPublicProfile(u, ""))
}
