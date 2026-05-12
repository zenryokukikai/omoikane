-- migration: 003_reverse_index
-- Phase 2 reverse-index tables and supporting FTS, per docs/design.md §4.2
-- and §13 Phase 2.

-- ============================================================
-- Symptoms index (reverse lookup: phrase → entry)
-- ============================================================
CREATE TABLE IF NOT EXISTS symptoms_index (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id          TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    phrase            TEXT NOT NULL,
    phrase_normalized TEXT NOT NULL,
    embedding         BLOB,                 -- Phase 4 reserves; NULL for Phase 2
    source            TEXT NOT NULL DEFAULT 'llm',  -- 'human'|'llm'|'heuristic'
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_sym_entry ON symptoms_index(entry_id);
CREATE INDEX IF NOT EXISTS idx_sym_norm  ON symptoms_index(phrase_normalized);

CREATE VIRTUAL TABLE IF NOT EXISTS symptoms_fts USING fts5(
    phrase,
    content='symptoms_index',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS symptoms_ai AFTER INSERT ON symptoms_index BEGIN
    INSERT INTO symptoms_fts(rowid, phrase) VALUES (new.id, new.phrase);
END;
CREATE TRIGGER IF NOT EXISTS symptoms_ad AFTER DELETE ON symptoms_index BEGIN
    INSERT INTO symptoms_fts(symptoms_fts, rowid, phrase) VALUES ('delete', old.id, old.phrase);
END;
CREATE TRIGGER IF NOT EXISTS symptoms_au AFTER UPDATE ON symptoms_index BEGIN
    INSERT INTO symptoms_fts(symptoms_fts, rowid, phrase) VALUES ('delete', old.id, old.phrase);
    INSERT INTO symptoms_fts(rowid, phrase) VALUES (new.id, new.phrase);
END;

-- ============================================================
-- Triggers index (reverse lookup: action → entry)
-- ============================================================
CREATE TABLE IF NOT EXISTS triggers_index (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id          TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    phrase            TEXT NOT NULL,
    phrase_normalized TEXT NOT NULL,
    domain            TEXT,                 -- preprocessing|training|inference|data|infra|other
    source            TEXT NOT NULL DEFAULT 'llm',
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_trg_entry  ON triggers_index(entry_id);
CREATE INDEX IF NOT EXISTS idx_trg_domain ON triggers_index(domain);
CREATE INDEX IF NOT EXISTS idx_trg_norm   ON triggers_index(phrase_normalized);

CREATE VIRTUAL TABLE IF NOT EXISTS triggers_fts USING fts5(
    phrase,
    content='triggers_index',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS triggers_ai AFTER INSERT ON triggers_index BEGIN
    INSERT INTO triggers_fts(rowid, phrase) VALUES (new.id, new.phrase);
END;
CREATE TRIGGER IF NOT EXISTS triggers_ad AFTER DELETE ON triggers_index BEGIN
    INSERT INTO triggers_fts(triggers_fts, rowid, phrase) VALUES ('delete', old.id, old.phrase);
END;
CREATE TRIGGER IF NOT EXISTS triggers_au AFTER UPDATE ON triggers_index BEGIN
    INSERT INTO triggers_fts(triggers_fts, rowid, phrase) VALUES ('delete', old.id, old.phrase);
    INSERT INTO triggers_fts(rowid, phrase) VALUES (new.id, new.phrase);
END;

-- ============================================================
-- Tag aliases (canonicalisation for tag normalisation)
-- ============================================================
CREATE TABLE IF NOT EXISTS tag_aliases (
    alias          TEXT PRIMARY KEY,
    canonical_tag  TEXT NOT NULL,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by     TEXT,
    notes          TEXT
);
CREATE INDEX IF NOT EXISTS idx_tag_aliases_canon ON tag_aliases(canonical_tag);

-- ============================================================
-- Trigger rules (dual-layer triggers: rule-based + LLM)
--
-- Each row is one rule loaded from trigger_rules.yaml (or supplied via
-- admin API) that maps a deterministic regex/keyword against an action
-- description to one or more entry ids. Used as a fast-path before the
-- LLM trigger lookup.
-- ============================================================
CREATE TABLE IF NOT EXISTS trigger_rules (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id     TEXT UNIQUE NOT NULL,   -- stable identifier from YAML
    pattern     TEXT NOT NULL,           -- regex (Go syntax)
    domain      TEXT,                    -- optional domain filter
    entry_ids   TEXT NOT NULL,           -- JSON array of entry IDs to surface
    priority    INTEGER NOT NULL DEFAULT 100,
    enabled     INTEGER NOT NULL DEFAULT 1,
    source      TEXT NOT NULL DEFAULT 'yaml',  -- 'yaml'|'admin'|'llm-promoted'
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_trig_rules_domain  ON trigger_rules(domain);
CREATE INDEX IF NOT EXISTS idx_trig_rules_enabled ON trigger_rules(enabled);
