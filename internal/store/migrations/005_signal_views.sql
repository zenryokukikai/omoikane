-- migration: 005_signal_views
-- Phase 3 — entry_signals (aggregate from usage_cases) + review_queue,
-- per docs/design.md §4.3.

CREATE VIEW IF NOT EXISTS entry_signals AS
SELECT
    e.id,
    e.project_id,
    e.title,
    e.type,
    e.status,
    COUNT(uc.case_id) AS total_uses,
    SUM(CASE WHEN uc.result = 'helpful' THEN 1 ELSE 0 END)            AS helpful_count,
    SUM(CASE WHEN uc.result = 'partially_helpful' THEN 1 ELSE 0 END)  AS partial_count,
    SUM(CASE WHEN uc.result = 'not_helpful' THEN 1 ELSE 0 END)        AS not_helpful_count,
    SUM(CASE WHEN uc.result = 'misleading' THEN 1 ELSE 0 END)         AS misleading_count,
    SUM(CASE WHEN uc.result = 'unknown' OR uc.result IS NULL THEN 1 ELSE 0 END) AS unknown_count,
    MAX(uc.retrieved_at) AS last_retrieved_at,
    CASE
        WHEN COUNT(uc.case_id) -
             SUM(CASE WHEN uc.result = 'unknown' OR uc.result IS NULL THEN 1 ELSE 0 END) = 0
        THEN NULL
        ELSE CAST(SUM(CASE
                WHEN uc.result = 'helpful' THEN 1.0
                WHEN uc.result = 'partially_helpful' THEN 0.5
                WHEN uc.result = 'misleading' THEN -1.0
                ELSE 0
            END) AS REAL)
            / (COUNT(uc.case_id) -
               SUM(CASE WHEN uc.result = 'unknown' OR uc.result IS NULL THEN 1 ELSE 0 END))
    END AS helpfulness_score
FROM entries e
LEFT JOIN usage_cases uc ON uc.entry_id = e.id
GROUP BY e.id;

CREATE VIEW IF NOT EXISTS review_queue AS
SELECT
    e.id,
    e.title,
    e.type,
    e.status,
    es.misleading_count,
    es.total_uses,
    es.helpfulness_score
FROM entries e
JOIN entry_signals es ON es.id = e.id
WHERE es.misleading_count >= 3
   OR (es.helpfulness_score IS NOT NULL AND es.helpfulness_score < -0.3)
   OR e.status = 'DRAFT'
ORDER BY es.misleading_count DESC, e.updated_at DESC;
