package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/kojira/omoikane/internal/store"
)

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyClientIP
)

// RequestID assigns a short hex ID. Echoed in X-Request-Id, available via
// RequestIDFrom and persisted in audit_log.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			var b [6]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}

// Recoverer converts any panic into a 500 INTERNAL response and logs the
// stack at error level.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic in handler",
						"request_id", RequestIDFrom(r.Context()),
						"path", r.URL.Path,
						"err", fmt.Sprintf("%v", rec),
						"stack", string(debug.Stack()))
					writeError(w, http.StatusInternalServerError, CodeInternal, "Internal server error", nil)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog logs one structured line per request.
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &statusRecorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(rw, r)
			logger.Info("http",
				"request_id", RequestIDFrom(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", rw.status,
				"bytes", rw.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
				"client_type", r.Header.Get("X-Client-Type"),
				"remote", clientIP(r),
			)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// LimitBody installs a per-request size cap. Reads past max return a 413.
func LimitBody(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Audit persists write requests to audit_log. Read requests are skipped —
// they would dominate the table and aren't required by §12.2.
//
// To avoid logging the entire body we capture only the first 256 bytes as
// a textual summary (sensitive values are already scrubbed by the secret
// scanner before reaching this point).
func Audit(s *store.Store, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isWriteRequest(r) || !strings.HasPrefix(r.URL.Path, "/v1/") {
				next.ServeHTTP(w, r)
				return
			}
			// Buffer the body for summarisation; restore for downstream.
			var summary string
			if r.Body != nil {
				const maxRead = 256
				buf := bytes.Buffer{}
				cap := io.LimitReader(r.Body, maxRead+1)
				if _, err := io.Copy(&buf, cap); err == nil {
					b := buf.Bytes()
					if len(b) > maxRead {
						summary = string(b[:maxRead]) + "...(truncated)"
					} else {
						summary = string(b)
					}
					// Concatenate read bytes with the remainder.
					remaining, _ := io.ReadAll(r.Body)
					r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(b), bytes.NewReader(remaining)))
				}
			}

			start := time.Now()
			rw := &statusRecorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(rw, r)

			// We can't pull the token off the context here — auth installs
			// it on a child request (r.WithContext) so the outer-middleware
			// `r` we hold never sees it. The auth middleware stashes the
			// audit-relevant fields on r.Header instead, which IS shared.
			ev := store.AuditEvent{
				Timestamp:   time.Now().UTC(),
				RequestID:   RequestIDFrom(r.Context()),
				Method:      r.Method,
				Path:        r.URL.Path,
				BodySummary: summary,
				ClientType:  r.Header.Get("X-Client-Type"),
				ClientIP:    clientIP(r),
				StatusCode:  rw.status,
				DurationMs:  time.Since(start).Milliseconds(),
				UserID:      r.Header.Get("X-Audit-User"),
				TokenName:   r.Header.Get("X-Audit-Token-Name"),
			}
			// Audit failures must not break the request lifecycle.
			if err := s.WriteAudit(r.Context(), ev); err != nil {
				logger.Warn("audit write failed",
					"request_id", ev.RequestID, "err", err)
			}
		})
	}
}

func isWriteRequest(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return v
	}
	return r.RemoteAddr
}
