-- Personal librarians (issue #73 slice A): each user may configure one
-- personal librarian agent that omoikane provisions onto the opencrab
-- agent runtime (id = 'plib-<user_id>'). This row is omoikane's record
-- of that provisioning — name/persona are what the user typed, status
-- is 'active' until slice C adds disable/delete.
--
-- Token idempotency is NOT tracked here: the librarian's kb token is a
-- normal api_tokens row (name 'personal-librarian') and its existence
-- is the single source of truth for "already issued" (no second flag
-- to drift out of sync).
CREATE TABLE IF NOT EXISTS user_librarians (
    user_id    TEXT PRIMARY KEY REFERENCES users(id),
    agent_id   TEXT NOT NULL,
    name       TEXT NOT NULL,
    persona    TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
