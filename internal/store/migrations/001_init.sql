-- migration: 001_init
-- Phase 1 schema per docs/design.md §4.2.1

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- Projects (multi-tenancy)
-- ============================================================
CREATE TABLE IF NOT EXISTS projects (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    metadata    TEXT
);

-- ============================================================
-- Entries: full v0.6 schema including temporal validity and OCC
-- ============================================================
CREATE TABLE IF NOT EXISTS entries (
    id                    TEXT PRIMARY KEY,
    project_id            TEXT NOT NULL REFERENCES projects(id),
    type                  TEXT NOT NULL,    -- trap|decision|design|lesson|incident
                                            --   |librarian_meta|external_finding
    title                 TEXT NOT NULL,
    status                TEXT NOT NULL DEFAULT 'DRAFT',
                                            -- DRAFT|INVESTIGATING|ACTIVE|SUPERSEDED
                                            --   |ARCHIVED|DUPLICATE|RESOLVED

    symptom               TEXT,
    root_cause            TEXT,
    resolution            TEXT,
    prohibited            TEXT,

    attempted_approaches  TEXT,
    observed_behavior     TEXT,
    hypotheses            TEXT,

    body                  TEXT NOT NULL,
    body_format           TEXT NOT NULL DEFAULT 'markdown',

    scope                 TEXT,

    -- Temporal validity (design §4.2.1)
    valid_from            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    valid_to              TIMESTAMP,
    superseded_by         TEXT REFERENCES entries(id),
    invalidation_reason   TEXT,

    -- Enrichment versioning
    enrichment_version    INTEGER NOT NULL DEFAULT 0,
    enrichment_at         TIMESTAMP,

    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by            TEXT,
    created_by_role       TEXT,             -- 'human'|'agent'|'librarian:<role>'

    -- Optimistic locking
    version               INTEGER NOT NULL DEFAULT 1,

    metadata              TEXT
);
CREATE INDEX IF NOT EXISTS idx_entries_project     ON entries(project_id);
CREATE INDEX IF NOT EXISTS idx_entries_type        ON entries(type);
CREATE INDEX IF NOT EXISTS idx_entries_status      ON entries(status);
CREATE INDEX IF NOT EXISTS idx_entries_type_status ON entries(type, status);
CREATE INDEX IF NOT EXISTS idx_entries_validity    ON entries(valid_from, valid_to);
CREATE INDEX IF NOT EXISTS idx_entries_superseded  ON entries(superseded_by);
CREATE INDEX IF NOT EXISTS idx_entries_updated     ON entries(updated_at DESC);

-- ============================================================
-- Tags
-- ============================================================
CREATE TABLE IF NOT EXISTS tags (
    entry_id   TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    tag        TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 1.0,
    source     TEXT NOT NULL DEFAULT 'llm',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (entry_id, tag)
);
CREATE INDEX IF NOT EXISTS idx_tags_tag ON tags(tag);

-- ============================================================
-- Entry history (full snapshot per version, supports ?as_of=)
-- ============================================================
CREATE TABLE IF NOT EXISTS entry_history (
    entry_id              TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    version               INTEGER NOT NULL,

    -- Mutable fields snapshot
    title                 TEXT NOT NULL,
    status                TEXT NOT NULL,
    symptom               TEXT,
    root_cause            TEXT,
    resolution            TEXT,
    prohibited            TEXT,
    attempted_approaches  TEXT,
    observed_behavior     TEXT,
    hypotheses            TEXT,
    body                  TEXT NOT NULL,
    body_format           TEXT NOT NULL,
    scope                 TEXT,
    metadata              TEXT,
    valid_from            TIMESTAMP NOT NULL,
    valid_to              TIMESTAMP,
    superseded_by         TEXT,
    invalidation_reason   TEXT,

    -- Tags snapshot (semicolon-joined for simplicity; restored as []string)
    tags_snapshot         TEXT,

    -- Change context
    changed_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    changed_by            TEXT,
    changed_by_role       TEXT,
    change_summary        TEXT,

    PRIMARY KEY (entry_id, version)
);
CREATE INDEX IF NOT EXISTS idx_history_changed_at ON entry_history(entry_id, changed_at);

-- ============================================================
-- Users and API tokens
-- ============================================================
CREATE TABLE IF NOT EXISTS users (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    role       TEXT NOT NULL DEFAULT 'member',  -- 'admin'|'member'|'agent'
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS api_tokens (
    token_hash    TEXT PRIMARY KEY,        -- SHA-256(plain)
    user_id       TEXT REFERENCES users(id),
    name          TEXT NOT NULL,
    scopes        TEXT NOT NULL,           -- comma-separated 'read,write,admin'
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at    TIMESTAMP,
    last_used_at  TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_id);

-- ============================================================
-- Audit log (active from Phase 1)
-- ============================================================
CREATE TABLE IF NOT EXISTS audit_log (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    request_id    TEXT,
    user_id       TEXT,
    token_name    TEXT,
    method        TEXT NOT NULL,
    path          TEXT NOT NULL,
    body_summary  TEXT,
    client_type   TEXT,
    client_ip     TEXT,
    status_code   INTEGER NOT NULL,
    duration_ms   INTEGER
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(timestamp DESC);
