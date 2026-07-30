-- Thread ownership: who opened the thread (users.id from the token).
-- Nullable — legacy librarian threads have no owner; the per-user chat
-- frontend filters on it (GET /v1/librarian/threads?mine=1).
ALTER TABLE chat_threads ADD COLUMN created_by TEXT;
CREATE INDEX IF NOT EXISTS idx_chat_threads_created_by ON chat_threads(created_by);
