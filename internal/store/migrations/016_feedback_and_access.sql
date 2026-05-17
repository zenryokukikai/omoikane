-- migration: 016_feedback_and_access
--
-- Replace the case_id-PATCH feedback mechanism (Phase 3 usage_cases) with
-- a lightweight entry_id-keyed feedback flow, and add passive access
-- logging.
--
-- Why this exists:
--   - usage_cases required agents to pass `create_cases: true` opt-in on
--     lookups, retain the returned case_id across calls, and then PATCH
--     it. Three-step protocol with stateful handoff. Result: zero feedback
--     in production (verified by inspection of usage_cases rows).
--   - access_log captures the FREE signal (entries that get fetched,
--     surfaced in searches/lookups) with no agent ceremony at all. This
--     is what `reference_count_30d` is derived from.
--   - entry_feedback captures the EXPLICIT signal (1-line POST) with no
--     case_id state to carry.
--
-- usage_cases is NOT dropped — it stays for the legacy data and for any
-- caller that explicitly opts into case tracking. But agent-facing
-- skill.md will direct new traffic to /v1/feedback.

-- ============================================================
-- entry_access_log: passive trace of "entry X was surfaced to caller Y"
-- ============================================================
CREATE TABLE IF NOT EXISTS entry_access_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id     TEXT    NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    user_id      TEXT,                          -- actor (nullable: anonymous/system access)
    source       TEXT    NOT NULL,              -- 'get' | 'search' | 'lookup_by_trigger' | 'lookup_by_symptom' | 'lookup_by_tags' | 'lookup_by_situation'
    query        TEXT,                          -- the user-supplied query for search/lookup; NULL for direct GET
    accessed_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_access_log_entry_time
    ON entry_access_log(entry_id, accessed_at DESC);
CREATE INDEX IF NOT EXISTS idx_access_log_user_time
    ON entry_access_log(user_id, accessed_at DESC);

-- ============================================================
-- entry_feedback: explicit per-entry signal from an agent.
-- One agent may file multiple feedback rows on the same entry over time
-- (e.g., "helpful" today, "outdated" three months later). No PK on
-- (entry_id, user_id) — we keep the full stream.
-- ============================================================
CREATE TABLE IF NOT EXISTS entry_feedback (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id    TEXT    NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    user_id     TEXT,                           -- actor (nullable: anonymous feedback path)
    signal      TEXT    NOT NULL CHECK (signal IN (
        'helpful',       -- applied or directly used in solving the task
        'confirmed',     -- already knew this, reinforced existing knowledge
        'outdated',      -- factually correct historically but no longer matches current state
        'wrong',         -- factually incorrect
        'incomplete',    -- correct but missing important context
        'surfaced_gap'   -- reading this revealed a gap in MY (the reader's) understanding
    )),
    context     TEXT,                           -- optional free-text justification (LLM-friendly)
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_feedback_entry_time
    ON entry_feedback(entry_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_feedback_user_time
    ON entry_feedback(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_feedback_signal
    ON entry_feedback(signal);

-- ============================================================
-- entry_engagement: composed view used by ranking / reasoning mode.
-- Combines passive access counts + explicit feedback signals.
-- Legacy usage_cases-based entry_signals view is left intact so existing
-- callers don't break.
-- ============================================================
CREATE VIEW IF NOT EXISTS entry_engagement AS
SELECT
    e.id AS entry_id,
    e.project_id,
    -- Passive: reference_count_30d
    COALESCE((
        SELECT COUNT(*) FROM entry_access_log al
         WHERE al.entry_id = e.id
           AND al.accessed_at > datetime('now', '-30 days')
    ), 0) AS reference_count_30d,
    -- Passive: all-time reference count (smoothing for low-traffic entries)
    COALESCE((
        SELECT COUNT(*) FROM entry_access_log al WHERE al.entry_id = e.id
    ), 0) AS reference_count_total,
    -- Explicit: per-signal counts
    COALESCE((SELECT COUNT(*) FROM entry_feedback f
              WHERE f.entry_id = e.id AND f.signal = 'helpful'), 0)      AS feedback_helpful,
    COALESCE((SELECT COUNT(*) FROM entry_feedback f
              WHERE f.entry_id = e.id AND f.signal = 'confirmed'), 0)    AS feedback_confirmed,
    COALESCE((SELECT COUNT(*) FROM entry_feedback f
              WHERE f.entry_id = e.id AND f.signal = 'outdated'), 0)     AS feedback_outdated,
    COALESCE((SELECT COUNT(*) FROM entry_feedback f
              WHERE f.entry_id = e.id AND f.signal = 'wrong'), 0)        AS feedback_wrong,
    COALESCE((SELECT COUNT(*) FROM entry_feedback f
              WHERE f.entry_id = e.id AND f.signal = 'incomplete'), 0)   AS feedback_incomplete,
    COALESCE((SELECT COUNT(*) FROM entry_feedback f
              WHERE f.entry_id = e.id AND f.signal = 'surfaced_gap'), 0) AS feedback_surfaced_gap,
    -- Composed score: positive signals weighted up, negative weighted down,
    -- normalized so first feedbacks don't dominate. Score is in roughly
    -- [-1, +1] but unbounded for very lopsided entries.
    --   helpful:      +1.0
    --   confirmed:    +0.3
    --   surfaced_gap: +0.5  (reading helped even if not by being right)
    --   outdated:     -0.4
    --   incomplete:   -0.2
    --   wrong:        -1.0
    CAST(
        COALESCE((SELECT COUNT(*) FROM entry_feedback f
                  WHERE f.entry_id = e.id AND f.signal = 'helpful'), 0)      *  1.0 +
        COALESCE((SELECT COUNT(*) FROM entry_feedback f
                  WHERE f.entry_id = e.id AND f.signal = 'confirmed'), 0)    *  0.3 +
        COALESCE((SELECT COUNT(*) FROM entry_feedback f
                  WHERE f.entry_id = e.id AND f.signal = 'surfaced_gap'), 0) *  0.5 +
        COALESCE((SELECT COUNT(*) FROM entry_feedback f
                  WHERE f.entry_id = e.id AND f.signal = 'outdated'), 0)     * -0.4 +
        COALESCE((SELECT COUNT(*) FROM entry_feedback f
                  WHERE f.entry_id = e.id AND f.signal = 'incomplete'), 0)   * -0.2 +
        COALESCE((SELECT COUNT(*) FROM entry_feedback f
                  WHERE f.entry_id = e.id AND f.signal = 'wrong'), 0)        * -1.0
        AS REAL
    ) / CAST(
        -- Smoothing denominator: 3 + total feedback count. With 0 feedback,
        -- score = 0/3 = 0 (neutral). With many feedbacks, denominator
        -- approaches total count so the score approaches the per-feedback
        -- mean weight.
        3 + COALESCE((SELECT COUNT(*) FROM entry_feedback f
                      WHERE f.entry_id = e.id), 0)
        AS REAL
    ) AS engagement_score
FROM entries e;
