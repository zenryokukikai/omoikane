-- migration: 008_ops
-- Phase 6 — tiered quality scoring + anomaly view
-- Phase 7 — backup_jobs / llm_usage_log + dead_pool helpers
-- per docs/design.md §13 Phase 6 + Phase 7.

-- ============================================================
-- Phase 6: tier scoring
-- ============================================================
--
-- Tier 1: entries with helpfulness >= 0.6 and >= 3 helpful cases
-- Tier 2: helpfulness >= 0.0 and >= 1 helpful case
-- Tier 3: present, no signal yet (DRAFT/ACTIVE with 0 cases)
-- Tier 4: negative signals (helpfulness < 0 or misleading_count >= 3)
CREATE VIEW IF NOT EXISTS entry_tiers AS
SELECT
    e.id,
    e.project_id,
    e.title,
    e.type,
    e.status,
    COALESCE(es.total_uses, 0)         AS total_uses,
    COALESCE(es.helpful_count, 0)      AS helpful_count,
    COALESCE(es.misleading_count, 0)   AS misleading_count,
    es.helpfulness_score,
    CASE
        WHEN COALESCE(es.helpfulness_score, 0) >= 0.6 AND COALESCE(es.helpful_count, 0) >= 3 THEN 1
        WHEN COALESCE(es.helpfulness_score, 0) >= 0.0 AND COALESCE(es.helpful_count, 0) >= 1 THEN 2
        WHEN COALESCE(es.helpfulness_score, 0) < 0    OR  COALESCE(es.misleading_count, 0) >= 3 THEN 4
        ELSE 3
    END AS tier
FROM entries e
LEFT JOIN entry_signals es ON es.id = e.id
WHERE e.status NOT IN ('SUPERSEDED','ARCHIVED','DUPLICATE');

-- ============================================================
-- Phase 7: ops tables
-- ============================================================

CREATE TABLE IF NOT EXISTS backup_jobs (
    id           TEXT PRIMARY KEY,
    path         TEXT NOT NULL,
    started_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at  TIMESTAMP,
    status       TEXT NOT NULL DEFAULT 'RUNNING',  -- RUNNING|DONE|FAILED
    bytes        INTEGER,
    error        TEXT
);
CREATE INDEX IF NOT EXISTS idx_backups_started ON backup_jobs(started_at);

CREATE TABLE IF NOT EXISTS llm_usage_log (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    provider      TEXT NOT NULL,
    model         TEXT,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd      REAL NOT NULL DEFAULT 0,
    purpose       TEXT,                       -- 'enrichment' | 'reasoning_search' | 'reflect' | …
    entry_id      TEXT,
    metadata      TEXT
);
CREATE INDEX IF NOT EXISTS idx_llm_usage_timestamp ON llm_usage_log(timestamp);

-- Helper view: dormant entries (no cases in the last 180 days, no
-- helpfulness signal). Phase 7's dead-pool cron archives these.
CREATE VIEW IF NOT EXISTS dormant_entries AS
SELECT
    e.id, e.project_id, e.type, e.title, e.status, e.updated_at,
    COALESCE(es.total_uses, 0) AS total_uses
FROM entries e
LEFT JOIN entry_signals es ON es.id = e.id
WHERE e.status = 'ACTIVE'
  AND COALESCE(es.total_uses, 0) = 0
  AND e.updated_at < datetime('now', '-180 days');
