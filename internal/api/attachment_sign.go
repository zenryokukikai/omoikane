package api

// Expiring signed attachment URLs (issue #104 slice G4).
//
// The agent-platform gateway delivers attachments as absolute https
// URLs that the platform fetches WITHOUT Authorization headers, so an
// auth-gated GET is unusable there. A presigned URL carries its own
// grant instead:
//
//	GET /v1/attachments/{id}/content?exp=<unix>&sig=<hex hmac>
//
// where sig = HMAC-SHA256(key, domain \n id \n exp). The signature is
// an ADDITIONAL grant, never a restriction: requests without exp/sig
// behave exactly as before, and an invalid or expired pair falls
// through to the normal auth path (which 401s/404s as today) — so a
// bad signature never becomes an existence oracle for attachment ids.
//
// The key comes from KB_URL_SIGNING_KEY; empty disables the feature
// entirely (issuance errors, verification never grants).

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// DefaultSignedURLTTL is the issuance default when the caller passes
// ttl <= 0. 15 minutes: long enough for the platform side to fetch a
// just-delivered message's attachments (including retries), short
// enough that a leaked URL goes stale before it is worth sharing.
const DefaultSignedURLTTL = 15 * time.Minute

// signedURLDomain domain-separates the MAC so the same key could later
// sign other URL families without cross-acceptance.
const signedURLDomain = "omoikane-attachment-v1"

// ErrSigningDisabled is returned by SignAttachmentURL when no signing
// key is configured (KB_URL_SIGNING_KEY unset).
var ErrSigningDisabled = errors.New("attachment URL signing disabled: KB_URL_SIGNING_KEY not configured")

// SignAttachmentURL mints a presigned relative URL (path + query) for
// the attachment's content endpoint. ttl <= 0 uses DefaultSignedURLTTL.
// The caller (future gateway code) prefixes the deployment's public
// base URL to make it absolute. Not exposed as an HTTP endpoint.
//
// Existence is NOT checked here — signing a bogus id just yields a URL
// that 404s. Callers sign ids they already hold from the store.
func (h *Handler) SignAttachmentURL(id string, ttl time.Duration) (string, error) {
	if len(h.URLSigningKey) == 0 {
		return "", ErrSigningDisabled
	}
	if id == "" {
		return "", errors.New("attachment id required")
	}
	if ttl <= 0 {
		ttl = DefaultSignedURLTTL
	}
	exp := time.Now().Add(ttl).Unix()
	sig := attachmentSig(h.URLSigningKey, id, exp)
	return fmt.Sprintf("/v1/attachments/%s/content?exp=%d&sig=%s",
		url.PathEscape(id), exp, hex.EncodeToString(sig)), nil
}

// attachmentSig computes the raw HMAC-SHA256 over (domain, id, exp).
func attachmentSig(key []byte, id string, exp int64) []byte {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s\n%s\n%d", signedURLDomain, id, exp)
	return mac.Sum(nil)
}

// verifyAttachmentSig reports whether (expStr, sigHex) is a valid,
// unexpired signature for attachment id at time now. False on any
// malformed input, on signature mismatch (constant-time compare), on
// expiry, or when the feature is disabled (empty key).
func (h *Handler) verifyAttachmentSig(id, expStr, sigHex string, now time.Time) bool {
	if len(h.URLSigningKey) == 0 || id == "" || expStr == "" || sigHex == "" {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || exp <= 0 {
		return false
	}
	got, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	if !hmac.Equal(got, attachmentSig(h.URLSigningKey, id, exp)) {
		return false
	}
	return now.Unix() <= exp
}

// signedAttachmentGrant is route-group middleware for the content GET.
// A valid, unexpired exp+sig pair serves the blob immediately, without
// auth: the request context has no space visibility installed, so the
// store view is unrestricted — the signature itself is the grant (it
// can only be minted server-side for a specific id). Anything else —
// no sig params, malformed, tampered, expired, feature off — falls
// through to next (the unchanged auth chain), leaking nothing about
// whether the attachment exists.
func (h *Handler) signedAttachmentGrant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		expStr, sigHex := q.Get("exp"), q.Get("sig")
		if expStr == "" && sigHex == "" {
			next.ServeHTTP(w, r) // no sig params: current behavior, untouched
			return
		}
		if !h.verifyAttachmentSig(chi.URLParam(r, "id"), expStr, sigHex, time.Now()) {
			next.ServeHTTP(w, r) // invalid/expired: normal auth path decides
			return
		}
		// Presigned responses must not land in shared caches, and the
		// platform side must not sniff past the declared mime.
		w.Header().Set("Cache-Control", "private, max-age=0")
		if w.Header().Get("X-Content-Type-Options") == "" {
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}
		h.getAttachmentContent(w, r)
	})
}
