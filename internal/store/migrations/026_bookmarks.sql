-- Per-user entry bookmarks (a human reading aid — agents retrieve via
-- search, humans keep shortlists). Composite PK makes toggling
-- idempotent; cascade keeps the table clean when entries/users go.
CREATE TABLE IF NOT EXISTS user_bookmarks (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    entry_id   TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, entry_id)
);
CREATE INDEX IF NOT EXISTS idx_bookmarks_user ON user_bookmarks(user_id, created_at DESC);
