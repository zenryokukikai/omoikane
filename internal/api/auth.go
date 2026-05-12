package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kojira/omoikane/internal/auth"
	"github.com/kojira/omoikane/internal/auth/oauth"
	"github.com/kojira/omoikane/internal/store"
)

const (
	stateCookieName   = "kb_oauth_state"
	sessionCookieName = "kb_session"
	stateCookieTTL    = 10 * time.Minute
)

// authGoogleLogin starts the OAuth flow: mint state, set cookie,
// redirect to Google.
//
// Before doing any of that, enforce that the user is on the same
// hostname as the configured OAuth redirect URI. Otherwise the state
// cookie is set on origin A but the callback arrives at origin B, and
// the browser correctly refuses to send it back. The fix is to bounce
// the user to the canonical host first.
func (h *Handler) authGoogleLogin(w http.ResponseWriter, r *http.Request) {
	if h.OAuthGoogle == nil {
		writeError(w, http.StatusNotImplemented, CodeNotImplemented,
			"Google OAuth is not configured on this server", nil)
		return
	}
	if canonicalHost := canonicalHostFromBase(h.OAuthRedirectBase); canonicalHost != "" && r.Host != canonicalHost {
		// Redirect to the canonical host with the same query string so
		// the cookie/callback origins match.
		scheme := "http"
		if strings.HasPrefix(h.OAuthRedirectBase, "https://") {
			scheme = "https"
		}
		dest := scheme + "://" + canonicalHost + r.URL.RequestURI()
		http.Redirect(w, r, dest, http.StatusFound)
		return
	}
	state, err := oauth.NewState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "state: "+err.Error(), nil)
		return
	}
	// Preserve a post-login redirect target (`?next=/path`). Restrict to
	// same-origin paths.
	next := r.URL.Query().Get("next")
	if !isSafeRedirect(next) {
		next = "/"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    state + ":" + next,
		Path:     "/",
		MaxAge:   int(stateCookieTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.HTTPSEnabled,
	})
	http.Redirect(w, r, h.OAuthGoogle.Authorize(state), http.StatusFound)
}

// authGoogleCallback completes the OAuth flow: verify state, exchange
// code, provision-or-link user, mint session.
func (h *Handler) authGoogleCallback(w http.ResponseWriter, r *http.Request) {
	if h.OAuthGoogle == nil {
		writeError(w, http.StatusNotImplemented, CodeNotImplemented,
			"Google OAuth is not configured on this server", nil)
		return
	}
	cookie, err := r.Cookie(stateCookieName)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"missing oauth state cookie", nil)
		return
	}
	clearStateCookie(w, h.HTTPSEnabled)

	parts := strings.SplitN(cookie.Value, ":", 2)
	expectedState := parts[0]
	next := "/"
	if len(parts) == 2 && isSafeRedirect(parts[1]) {
		next = parts[1]
	}
	if r.URL.Query().Get("state") != expectedState || expectedState == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "state mismatch", nil)
		return
	}
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"google reported error: "+errParam, nil)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "code missing", nil)
		return
	}

	id, err := h.OAuthGoogle.Callback(r.Context(), code)
	if err != nil {
		h.Logger.Warn("oauth callback failed", "err", err)
		writeError(w, http.StatusUnauthorized, CodeInvalidToken,
			"oauth callback: "+err.Error(), nil)
		return
	}
	if !oauth.EmailAllowed(id.Email, h.AuthAllowDomains, h.AuthAllowEmails) {
		h.Logger.Warn("login denied by allow-list", "email", id.Email)
		writeError(w, http.StatusForbidden, CodeForbidden,
			"this email is not permitted to sign in. Contact an administrator.",
			map[string]any{"email": id.Email})
		return
	}

	user, err := h.Store.ProvisionGoogleUser(httpCtx(r), id.Email, id.Subject, id.Name, id.AvatarURL)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	_ = h.Store.RecordLogin(httpCtx(r), user.ID)

	// Session-type token; agent tokens (`api`) are issued separately by
	// the admin CLI. Session scopes mirror the user's role: admin gets
	// admin, others get read+write.
	scopes := []string{"read", "write"}
	if user.Role == "admin" {
		scopes = append(scopes, "admin")
	}
	plain, err := h.Store.CreateSessionToken(httpCtx(r), user.ID, "session", scopes, h.SessionTTL)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// Drop the session into both a cookie (browser) and the redirect
	// URL (so the existing `?token=…` dashboard auth works). Cookie is
	// the primary path going forward; the query-string fallback keeps
	// CLI bookmarks etc. working.
	maxAge := int(h.SessionTTL.Seconds())
	if h.SessionTTL <= 0 {
		maxAge = int((24 * time.Hour).Seconds())
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    plain,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.HTTPSEnabled,
	})
	// Append/replace ?token= on the next URL.
	dest := appendTokenQuery(next, plain)
	http.Redirect(w, r, dest, http.StatusFound)
}

// authMe returns the current authenticated user. Honours either the
// session cookie or a Bearer token.
func (h *Handler) authMe(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken,
			"no authenticated user", nil)
		return
	}
	u, err := h.Store.GetUser(httpCtx(r), tok.UserID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":  u,
		"token": map[string]any{"name": tok.Name, "scopes": tok.Scopes, "type": tok.TokenType},
	})
}

// authLogout revokes the current session token and clears the cookie.
// Bearer tokens issued separately (agent tokens) are unaffected.
func (h *Handler) authLogout(w http.ResponseWriter, r *http.Request) {
	// Extract the token from either header or cookie so we know what to
	// revoke. Don't fail loudly if the user has no session.
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		_ = h.Store.RevokeToken(httpCtx(r), c.Value)
	}
	// Clear the session cookie regardless
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.HTTPSEnabled,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func clearStateCookie(w http.ResponseWriter, https bool) {
	http.SetCookie(w, &http.Cookie{
		Name: stateCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: https,
	})
}

// canonicalHostFromBase extracts the host part of KB_OAUTH_REDIRECT_BASE
// (e.g. "http://localhost:8095" → "localhost:8095"). Returns "" when
// the base is empty or unparseable.
func canonicalHostFromBase(base string) string {
	if base == "" {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return u.Host
}

// isSafeRedirect rejects external / scheme-bearing targets. We only
// allow same-origin absolute paths.
func isSafeRedirect(path string) bool {
	if path == "" {
		return false
	}
	if strings.HasPrefix(path, "//") {
		return false // protocol-relative URL
	}
	u, err := url.Parse(path)
	if err != nil {
		return false
	}
	if u.Scheme != "" || u.Host != "" {
		return false
	}
	return strings.HasPrefix(u.Path, "/")
}

// appendTokenQuery puts `token=<plain>` on the URL, replacing any
// pre-existing value. Used as a fallback so old `?token=` bookmarks keep
// working post-login.
func appendTokenQuery(rawURL, token string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String()
}

// silence unused warnings if store sentinel paths change
var _ = errors.Is
var _ = store.ErrNotFound
