package store

import (
	"context"
	"time"
)

// WriteAudit persists an audit_log row. Used by the API audit middleware on
// every write request (POST/PATCH/DELETE).
func (s *Store) WriteAudit(ctx context.Context, ev AuditEvent) error {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_log(timestamp, request_id, user_id, token_name,
		                     method, path, body_summary, client_type, client_ip,
		                     status_code, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.Timestamp, nullable(ev.RequestID), nullable(ev.UserID), nullable(ev.TokenName),
		ev.Method, ev.Path, nullable(ev.BodySummary),
		nullable(ev.ClientType), nullable(ev.ClientIP),
		ev.StatusCode, ev.DurationMs,
	)
	return translateErr(err)
}
