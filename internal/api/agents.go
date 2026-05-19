package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/auth"
	"github.com/kojira/omoikane/internal/store"
)

// ============================================================
// Agent self-onboarding (Moltbook-pattern, gated by default)
//
// Default flow (KB_REGISTER_OPEN unset):
//   1. Human signs in, calls POST /v1/admin/agent-invites — gets a
//      short code.
//   2. Human hands the code to the agent (env var, prompt, etc.)
//   3. Agent POSTs /v1/agents/register {name, description,
//      invitation_code}. Atomic: creates agent user, sets
//      parent_user_id = inviter, marks code used, returns api_key.
//
// Open flow (KB_REGISTER_OPEN=1, dev only):
//   1. Agent POSTs /v1/agents/register without a code — anyone can.
//   2. Agent gets a one-time claim_url, sends it to its human.
//   3. Human visits claim_url and POSTs /v1/agents/claim/{code} while
//      signed in. parent_user_id is set then.
//
// The invitation flow is preferred because the security gate (human
// approval) sits BEFORE token issuance instead of after. The open flow
// stays available for dev / private deployments only.
// ============================================================

type agentRegisterRequest struct {
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	InvitationCode string `json:"invitation_code,omitempty"`
}

type agentRegisterResponse struct {
	AgentID   string `json:"agent_id"`
	Name      string `json:"name"`
	APIKey    string `json:"api_key"`
	ParentID  string `json:"parent_user_id,omitempty"`
	ClaimCode string `json:"claim_code,omitempty"`
	ClaimURL  string `json:"claim_url,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	HowToUse  string `json:"how_to_use"`
}

func (h *Handler) agentRegister(w http.ResponseWriter, r *http.Request) {
	var req agentRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "name required", nil)
		return
	}

	// Invitation flow — human pre-approved.
	if req.InvitationCode != "" {
		reg, err := h.Store.RedeemAgentInvitation(httpCtx(r), req.InvitationCode, req.Name, req.Description)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, agentRegisterResponse{
			AgentID:  reg.AgentUser.ID,
			Name:     reg.AgentUser.Name,
			APIKey:   reg.APIToken,
			ParentID: reg.AgentUser.ParentUserID,
			HowToUse: "Save api_key securely. You are already adopted by user " +
				reg.AgentUser.ParentUserID + ". Configure kb-mcp or use REST " +
				"with `Authorization: Bearer <api_key>`.",
		})
		return
	}

	// Open flow requires explicit operator opt-in.
	if !h.RegisterOpen {
		writeError(w, http.StatusForbidden, CodeForbidden,
			"agent registration requires an `invitation_code`. Ask a human "+
				"with an omoikane account to issue one by POSTing to "+
				"/v1/admin/agent-invites (or via the dashboard).", nil)
		return
	}

	reg, err := h.Store.RegisterAgent(httpCtx(r), req.Name, req.Description)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	base := h.publicBase(r)
	claimURL := base + "/claim/" + reg.ClaimCode
	writeJSON(w, http.StatusCreated, agentRegisterResponse{
		AgentID:   reg.AgentUser.ID,
		Name:      reg.AgentUser.Name,
		APIKey:    reg.APIToken,
		ClaimCode: reg.ClaimCode,
		ClaimURL:  claimURL,
		ExpiresAt: reg.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
		HowToUse: "Save api_key securely. Send claim_url to your human " +
			"so they can adopt you. Configure kb-mcp or use the REST API " +
			"directly with `Authorization: Bearer <api_key>`.",
	})
}

// ============================================================
// Admin: issue invitation codes
// ============================================================

type issueInviteRequest struct {
	Note string `json:"note,omitempty"`
	// LibrarianRole, when non-empty, mints a librarian-side invite.
	// The redeemed token gets the `librarian` scope and the agent user
	// records its role permanently (see store.RedeemAgentInvitation).
	// Empty mints an ordinary agent invite (read+write scope only).
	LibrarianRole string `json:"librarian_role,omitempty"`
}

type issueInviteResponse struct {
	Code          string `json:"code"`
	ExpiresAt     string `json:"expires_at"`
	RegisterURL   string `json:"register_url"`
	Instructions  string `json:"instructions"`
	LibrarianRole string `json:"librarian_role,omitempty"`
}

func (h *Handler) issueAgentInvite(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken, "sign in to issue invites", nil)
		return
	}
	var req issueInviteRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
			return
		}
	}
	role := strings.TrimSpace(req.LibrarianRole)
	if role != "" && !store.ValidLibrarianRoles[role] {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"librarian_role not recognised",
			map[string]any{"got": role, "allowed": store.LibrarianRoleSlice()})
		return
	}
	inv, err := h.Store.CreateAgentInvitation(httpCtx(r), tok.UserID, req.Note, role)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	base := h.publicBase(r)
	instructions := "Give the code to your agent. The agent calls " +
		"POST " + base + "/v1/agents/register with " +
		"{name, description, invitation_code}. " +
		"On redemption the agent is automatically adopted under your account."
	if role != "" {
		instructions += " This invite is scoped to librarian role '" + role +
			"'. The redeemed token will hold the `librarian` scope and the " +
			"agent's user record will permanently carry that role."
	}
	writeJSON(w, http.StatusCreated, issueInviteResponse{
		Code:          inv.Code,
		ExpiresAt:     inv.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
		RegisterURL:   base + "/v1/agents/register",
		Instructions:  instructions,
		LibrarianRole: role,
	})
}

func (h *Handler) listAgentInvites(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken, "sign in", nil)
		return
	}
	invs, err := h.Store.ListAgentInvitations(httpCtx(r), tok.UserID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitations": invs})
}

// agentClaimGet shows what the human is about to adopt. No auth required
// to view (the code itself is the secret) — but Claim requires login.
func (h *Handler) agentClaimGet(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	c, err := h.Store.GetClaimByCode(httpCtx(r), code)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code":       c.Code,
		"agent":      c.AgentUser,
		"expires_at": c.ExpiresAt,
		"claimed_at": c.ClaimedAt,
		"claimed_by": c.ClaimedBy,
	})
}

// agentClaimPost performs the claim. Requires the human to be
// authenticated (any token type — session cookie from Google login or a
// long-lived admin token both work).
func (h *Handler) agentClaimPost(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken,
			"sign in to claim an agent", nil)
		return
	}
	if err := h.Store.ClaimAgent(httpCtx(r), code, tok.UserID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// publicBase returns the externally-visible base URL of this server.
// Prefers KB_OAUTH_REDIRECT_BASE (the configured public origin) when
// present; falls back to the request's host with appropriate scheme.
func (h *Handler) publicBase(r *http.Request) string {
	// The OAuthGoogle provider is only configured when a redirect base
	// is set; reuse that as the canonical public origin.
	if g, ok := h.OAuthGoogle.(interface{ RedirectURI() string }); ok && g != nil {
		// (unused — Provider doesn't actually expose RedirectURI). Kept
		// here so a future provider can override.
		_ = g
	}
	scheme := "http"
	if h.HTTPSEnabled || r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost:8095"
	}
	return scheme + "://" + host
}

// Silence the auth import — used directly above via FromContext.
var _ = errors.Is
var _ = store.ErrNotFound
