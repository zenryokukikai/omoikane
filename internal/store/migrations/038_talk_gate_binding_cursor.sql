-- Reconnect catch-up cursor for /talk gate bindings (issue #104 G3a).
--
-- last_sent_message_id is the newest librarian_chat message id the
-- gateway confirmed dispatching for this thread. On reconnect the gate
-- binary resumes from here (at-least-once; the effect side dedups by
-- origin message id), so a gap between SSE sessions is replayed instead
-- of lost. '' = nothing dispatched yet (start from the beginning).
--
-- NOTE: shipped migration version numbers are never reusable.
ALTER TABLE talk_gate_bindings ADD COLUMN last_sent_message_id TEXT NOT NULL DEFAULT '';
