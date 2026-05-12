-- migration: 004_feedback_relations
-- Phase 3 — usage_cases, relations, situations, incident_clusters and
-- their associative tables, per docs/design.md §4.2.

-- ============================================================
-- Usage cases (feedback loop)
-- ============================================================
CREATE TABLE IF NOT EXISTS usage_cases (
    case_id              TEXT PRIMARY KEY,
    entry_id             TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    project_id           TEXT REFERENCES projects(id),

    client_type          TEXT,
    client_version       TEXT,
    session_id           TEXT,
    agent_role           TEXT,
    agent_label          TEXT,

    retrieved_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    trigger_query        TEXT,
    task_context         TEXT,
    environment          TEXT,

    outcome              TEXT,                -- applied|considered_rejected|ignored|NULL
    application_detail   TEXT,
    rejection_reason     TEXT,

    result               TEXT,                -- helpful|partially_helpful|not_helpful|misleading|unknown|NULL
    result_evidence      TEXT,
    result_judged_by     TEXT,
    result_judged_at     TIMESTAMP,

    notes                TEXT,
    metadata             TEXT
);
CREATE INDEX IF NOT EXISTS idx_uc_entry     ON usage_cases(entry_id);
CREATE INDEX IF NOT EXISTS idx_uc_outcome   ON usage_cases(outcome);
CREATE INDEX IF NOT EXISTS idx_uc_result    ON usage_cases(result);
CREATE INDEX IF NOT EXISTS idx_uc_session   ON usage_cases(session_id);
CREATE INDEX IF NOT EXISTS idx_uc_retrieved ON usage_cases(retrieved_at);

-- ============================================================
-- Relations (graph of entry → entry)
-- ============================================================
CREATE TABLE IF NOT EXISTS relations (
    from_id     TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    to_id       TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    rel_type    TEXT NOT NULL,           -- related|supersedes|conflicts_with|depends_on|see_also|duplicate_of|resolved_by
    confidence  REAL NOT NULL DEFAULT 1.0,
    source      TEXT NOT NULL DEFAULT 'llm',
    notes       TEXT,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (from_id, to_id, rel_type)
);
CREATE INDEX IF NOT EXISTS idx_rel_to   ON relations(to_id);
CREATE INDEX IF NOT EXISTS idx_rel_type ON relations(rel_type);

-- ============================================================
-- Situations (reverse-dictionary headings)
-- ============================================================
CREATE TABLE IF NOT EXISTS situations (
    id          TEXT PRIMARY KEY,
    project_id  TEXT REFERENCES projects(id),
    description TEXT NOT NULL,
    domain      TEXT,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    metadata    TEXT
);
CREATE INDEX IF NOT EXISTS idx_situations_project ON situations(project_id);

CREATE TABLE IF NOT EXISTS situation_entries (
    situation_id TEXT NOT NULL REFERENCES situations(id) ON DELETE CASCADE,
    entry_id     TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    relevance    REAL DEFAULT 1.0,
    notes        TEXT,
    PRIMARY KEY (situation_id, entry_id)
);

CREATE VIRTUAL TABLE IF NOT EXISTS situations_fts USING fts5(
    description,
    content='situations',
    content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS situations_ai AFTER INSERT ON situations BEGIN
    INSERT INTO situations_fts(rowid, description) VALUES (new.rowid, new.description);
END;
CREATE TRIGGER IF NOT EXISTS situations_ad AFTER DELETE ON situations BEGIN
    INSERT INTO situations_fts(situations_fts, rowid, description)
    VALUES ('delete', old.rowid, old.description);
END;
CREATE TRIGGER IF NOT EXISTS situations_au AFTER UPDATE ON situations BEGIN
    INSERT INTO situations_fts(situations_fts, rowid, description)
    VALUES ('delete', old.rowid, old.description);
    INSERT INTO situations_fts(rowid, description) VALUES (new.rowid, new.description);
END;

-- ============================================================
-- Incident clusters
-- ============================================================
CREATE TABLE IF NOT EXISTS incident_clusters (
    id                     TEXT PRIMARY KEY,
    project_id             TEXT REFERENCES projects(id),
    title                  TEXT NOT NULL,
    summary                TEXT,
    member_count           INTEGER NOT NULL DEFAULT 0,
    promoted_to_entry_id   TEXT REFERENCES entries(id),
    status                 TEXT NOT NULL DEFAULT 'OPEN',  -- OPEN|PROMOTED|DISMISSED
    created_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    metadata               TEXT
);
CREATE INDEX IF NOT EXISTS idx_clusters_project ON incident_clusters(project_id);
CREATE INDEX IF NOT EXISTS idx_clusters_status  ON incident_clusters(status);

CREATE TABLE IF NOT EXISTS incident_cluster_members (
    cluster_id  TEXT NOT NULL REFERENCES incident_clusters(id) ON DELETE CASCADE,
    entry_id    TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    similarity  REAL DEFAULT 1.0,
    added_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    added_by    TEXT,
    PRIMARY KEY (cluster_id, entry_id)
);
