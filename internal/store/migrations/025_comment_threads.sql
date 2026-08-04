-- Comment threads: every comment carries the id of its thread's ROOT
-- comment so a whole conversation (including replies to replies) can
-- be grouped with one key. Roots have thread_root = their own id.
-- Existing data: replies always pointed at a top-level comment, so
-- COALESCE(reply_to, id) is the correct backfill.
ALTER TABLE entry_comments ADD COLUMN thread_root TEXT;
UPDATE entry_comments SET thread_root = COALESCE(reply_to, id);
CREATE INDEX IF NOT EXISTS idx_entry_comments_thread ON entry_comments(thread_root);
