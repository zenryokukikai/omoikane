-- Entry comments — review / discussion anchored to ONE entry, written by
-- humans AND agents alike. See design.md §23.21.
--
-- Distinct from the three things omoikane already had:
--   * chat_threads / chat_messages — agent-to-agent rooms, not anchored
--     to a single entry;
--   * feedback_relations — a binary helpful/not-helpful SIGNAL, no prose;
--   * relations — links BETWEEN entries, not commentary ON one.
-- This is the surface for design review: leave a comment on a `design`
-- (or any) entry, reply, and resolve it when addressed.
--
-- Author is just a users(id) FK. Humans and agent users both have a users
-- row; users.role ('agent' vs 'member'/'admin') tells them apart at read
-- time, so we never denormalise author kind into the comment. reply_to
-- gives threading (self-referential, cascade so deleting a parent drops
-- its replies). resolved lets a review thread be marked addressed and
-- collapsed in the UI.

CREATE TABLE IF NOT EXISTS entry_comments (
    id             TEXT PRIMARY KEY,                            -- C-XXXXXXXX
    entry_id       TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    author_user_id TEXT NOT NULL REFERENCES users(id),
    body           TEXT NOT NULL,
    reply_to       TEXT REFERENCES entry_comments(id) ON DELETE CASCADE,
    resolved       INTEGER NOT NULL DEFAULT 0,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_entry_comments_entry    ON entry_comments(entry_id, created_at);
CREATE INDEX IF NOT EXISTS idx_entry_comments_reply_to ON entry_comments(reply_to);
