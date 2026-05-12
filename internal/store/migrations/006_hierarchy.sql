-- migration: 006_hierarchy
-- Phase 4 — hierarchy_nodes / hierarchy_entries / derived_summaries
-- per docs/design.md §4.2.4 + §13 Phase 4.

CREATE TABLE IF NOT EXISTS hierarchy_nodes (
    id           TEXT PRIMARY KEY,
    project_id   TEXT REFERENCES projects(id),
    parent_id    TEXT REFERENCES hierarchy_nodes(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    description  TEXT,
    sort_order   INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    metadata     TEXT
);
CREATE INDEX IF NOT EXISTS idx_hier_project ON hierarchy_nodes(project_id);
CREATE INDEX IF NOT EXISTS idx_hier_parent  ON hierarchy_nodes(parent_id);

-- Many-to-many: an entry may live under multiple hierarchy paths.
CREATE TABLE IF NOT EXISTS hierarchy_entries (
    node_id   TEXT NOT NULL REFERENCES hierarchy_nodes(id) ON DELETE CASCADE,
    entry_id  TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    weight    REAL NOT NULL DEFAULT 1.0,
    added_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    added_by  TEXT,
    PRIMARY KEY (node_id, entry_id)
);
CREATE INDEX IF NOT EXISTS idx_hentries_entry ON hierarchy_entries(entry_id);

-- Periodic summaries derived from entries under a node (or other source).
CREATE TABLE IF NOT EXISTS derived_summaries (
    id            TEXT PRIMARY KEY,
    source_type   TEXT NOT NULL,             -- 'hierarchy_node' | 'tag' | 'search'
    source_key    TEXT NOT NULL,             -- node_id / tag / query
    title         TEXT NOT NULL,
    summary       TEXT NOT NULL,
    entry_count   INTEGER NOT NULL DEFAULT 0,
    generated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    generated_by  TEXT,                      -- 'heuristic' | 'llm:<model>' | 'human'
    metadata      TEXT
);
CREATE INDEX IF NOT EXISTS idx_summary_source ON derived_summaries(source_type, source_key);
CREATE INDEX IF NOT EXISTS idx_summary_generated ON derived_summaries(generated_at);
