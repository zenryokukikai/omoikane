-- Operator directives for librarian roles (issue #31). First consumer:
-- scout — humans register "watch this topic" from the UI and the scout
-- expands its patrol with targeted searches ON TOP of the normal
-- feeds (never narrowing them). Kept role-generic so other librarians
-- can consume directives later.
CREATE TABLE IF NOT EXISTS librarian_directives (
    id         TEXT PRIMARY KEY,
    role       TEXT NOT NULL,
    text       TEXT NOT NULL,
    created_by TEXT NOT NULL REFERENCES users(id),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    active     INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_directives_role_active
    ON librarian_directives(role, active);
