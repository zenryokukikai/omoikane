// Package auth provides Bearer-token authentication middleware backed by the
// store layer. Tokens are looked up by their SHA-256 hash; scopes are
// checked per-route via RequireScope.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/zenryokukikai/omoikane/internal/store"
)

type ctxKey int

const (
	ctxKeyToken ctxKey = iota
)

// Middleware authenticates Bearer tokens against the store.
type Middleware struct {
	S *store.Store
}

// FromContext returns the authenticated token, or nil for unauthenticated
// requests (e.g. /v1/health).
func FromContext(ctx context.Context) *store.APIToken {
	t, _ := ctx.Value(ctxKeyToken).(*store.APIToken)
	return t
}

// Authenticate requires a valid Bearer token and attaches the looked-up
// APIToken to the context.
func (m *Middleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := extractBearer(r)
		if err != nil {
			writeAuthError(w, "MISSING_TOKEN", "Authorization Bearer header required", http.StatusUnauthorized)
			return
		}
		tok, err := m.S.LookupToken(r.Context(), token)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeAuthError(w, "INVALID_TOKEN", "Token not recognised or expired", http.StatusUnauthorized)
				return
			}
			writeAuthError(w, "INTERNAL", "Authentication backend error", http.StatusInternalServerError)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyToken, tok)
		// Stash audit-relevant fields on the *Request itself so outer
		// middleware (chi.Use'd before this sub-router's Mount) can read
		// them — request.Header is a shared mutable struct, unlike the
		// context which is replaced by WithValue.
		if tok.UserID != "" {
			r.Header.Set("X-Audit-User", tok.UserID)
		}
		r.Header.Set("X-Audit-Token-Name", tok.Name)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireScope returns middleware enforcing the named scope. "admin"
// implicitly satisfies any other scope.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := FromContext(r.Context())
			if tok == nil {
				writeAuthError(w, "MISSING_TOKEN", "Authentication required", http.StatusUnauthorized)
				return
			}
			if !store.HasScope(tok.Scopes, scope) {
				writeAuthErrorDetails(w, "FORBIDDEN",
					"Token lacks required scope: "+scope, http.StatusForbidden,
					map[string]any{"required": scope, "have": tok.Scopes})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SessionCookieToBearer promotes a session cookie (set by the OAuth
// callback flow) into an Authorization: Bearer header so the existing
// authentication middleware processes it identically to API tokens.
// Order before Authenticate; matched cookie name comes from
// api.sessionCookieName.
func SessionCookieToBearer(cookieName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "" {
				if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
					r.Header.Set("Authorization", "Bearer "+c.Value)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// AllowQueryTokenForGET lets the dashboard pass tokens via ?token=… for GET
// requests only (development convenience). Order before Authenticate.
func AllowQueryTokenForGET(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.Header.Get("Authorization") == "" {
			if t := r.URL.Query().Get("token"); t != "" {
				r.Header.Set("Authorization", "Bearer "+t)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// AllowQueryTokenAny is the same as AllowQueryTokenForGET but also
// promotes the token for POST/PATCH/DELETE — needed for the chat-room
// form submissions in the dashboard. Browsers can't set custom headers
// on plain form POSTs, so we accept the token via query string here.
// Risk acknowledged: this opens a CSRF vector if the dashboard runs on
// a shared host with cookie-bearing third-party content. For Phase 5's
// single-user local dashboard, the tradeoff is acceptable.
func AllowQueryTokenAny(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			if t := r.URL.Query().Get("token"); t != "" {
				r.Header.Set("Authorization", "Bearer "+t)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func extractBearer(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", errors.New("missing Authorization header")
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", errors.New("malformed Authorization header")
	}
	tok := strings.TrimSpace(parts[1])
	if tok == "" {
		return "", errors.New("empty token")
	}
	return tok, nil
}

func writeAuthError(w http.ResponseWriter, code, message string, status int) {
	writeAuthErrorDetails(w, code, message, status, nil)
}

func writeAuthErrorDetails(w http.ResponseWriter, code, message string, status int, details any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	envelope := map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	if details != nil {
		envelope["error"].(map[string]any)["details"] = details
	}
	_ = json.NewEncoder(w).Encode(envelope)
}
