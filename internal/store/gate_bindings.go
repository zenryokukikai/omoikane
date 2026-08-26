package store

// External gate provisioning state (issue #104 slice G2): the per-user
// gate instance id on user_librarians, and the thread ↔ binding
// correspondence table written at thread-creation time (slice G3).

import (
	"context"
	"time"
)

// TalkGateBinding is one /talk thread's gate binding registration.
type TalkGateBinding struct {
	ThreadID   string    `json:"thread_id"`
	BindingID  string    `json:"binding_id"`
	InstanceID string    `json:"instance_id"`
	CreatedAt  time.Time `json:"created_at"`
	// LastSentMessageID is the reconnect catch-up cursor (issue #104
	// G3a): the newest librarian_chat message id the gateway confirmed
	// dispatching for this thread. "" = nothing dispatched yet.
	LastSentMessageID string `json:"last_sent_message_id"`
}

// SetUserLibrarianGateInstance records the gate instance registered for
// userID's personal librarian. ErrNotFound when the user has no
// librarian row (the instance is registered after provisioning, so the
// row must already exist).
func (s *Store) SetUserLibrarianGateInstance(ctx context.Context, userID, instanceID string) error {
	if userID == "" || instanceID == "" {
		return ErrInvalidInput
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE user_librarians SET gate_instance_id = ?
		 WHERE user_id = ?`, instanceID, userID)
	if err != nil {
		return translateErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetTalkGateBinding returns the gate binding registered for threadID,
// or ErrNotFound.
func (s *Store) GetTalkGateBinding(ctx context.Context, threadID string) (*TalkGateBinding, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT thread_id, binding_id, instance_id, created_at, last_sent_message_id
		  FROM talk_gate_bindings WHERE thread_id = ?`, threadID)
	var b TalkGateBinding
	if err := row.Scan(&b.ThreadID, &b.BindingID, &b.InstanceID, &b.CreatedAt,
		&b.LastSentMessageID); err != nil {
		return nil, translateErr(err)
	}
	return &b, nil
}

// SetTalkGateBindingCursor records the newest message id the gateway
// dispatched for threadID (the reconnect catch-up cursor, issue #104
// G3a). ErrNotFound when the thread has no binding row — the cursor
// only ever trails an existing binding.
func (s *Store) SetTalkGateBindingCursor(ctx context.Context, threadID, messageID string) error {
	if threadID == "" || messageID == "" {
		return ErrInvalidInput
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE talk_gate_bindings SET last_sent_message_id = ?
		 WHERE thread_id = ?`, messageID, threadID)
	if err != nil {
		return translateErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PutTalkGateBinding records (or replaces) the gate binding for a
// thread. Replace covers the admin plane's address-reuse rule: a
// closed binding is never reopened, a fresh binding id takes over the
// same thread address.
func (s *Store) PutTalkGateBinding(ctx context.Context, threadID, bindingID, instanceID string) error {
	if threadID == "" || bindingID == "" || instanceID == "" {
		return ErrInvalidInput
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO talk_gate_bindings(thread_id, binding_id, instance_id)
		VALUES (?, ?, ?)
		ON CONFLICT(thread_id) DO UPDATE SET
			binding_id  = excluded.binding_id,
			instance_id = excluded.instance_id`,
		threadID, bindingID, instanceID)
	return translateErr(err)
}

// DeleteTalkGateBinding removes the thread's binding row. Idempotent —
// deleting an absent row is a no-op.
func (s *Store) DeleteTalkGateBinding(ctx context.Context, threadID string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM talk_gate_bindings WHERE thread_id = ?`, threadID)
	return translateErr(err)
}
