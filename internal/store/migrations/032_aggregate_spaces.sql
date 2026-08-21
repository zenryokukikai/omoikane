-- Aggregates + attachments join the space read-ACL (issue #60, Phase 1
-- slice 3). NB: numbered 032 because version 30 was burned in
-- production (see the note atop 031_spaces.sql) — never reuse a version
-- number that ever shipped.
--
-- Design v2: aggregates are NOT projections — a situation / cluster /
-- hierarchy node / use_case carries content of its own (name, summary,
-- derived text), so each aggregate belongs to exactly ONE space and
-- every entry linked into it must live in that same space (enforced by
-- the store at link time; violation = not-found, never a 403 oracle).
-- Attachments carry their space directly (v1's "parent entry" rule was
-- unimplementable — there is no parent-entry FK).
--
-- Existing rows land in 'internal' via the column DEFAULT
-- (behaviour-preserving migration; all pre-slice-3 data is internal).
--
-- Deployment verification (operator note — NOT executed here): links
-- written between the slice-2 and slice-3 deploys could cross spaces
-- (the same-space invariant only guards new writes). Each count below
-- must be 0; a non-zero row is a cross-space link to re-home by hand:
--
--   SELECT 'situation_entries' t, COUNT(*) n FROM situation_entries l
--     JOIN situations a ON a.id = l.situation_id
--     JOIN entries e ON e.id = l.entry_id WHERE e.space_id != a.space_id
--   UNION ALL
--   SELECT 'incident_cluster_members', COUNT(*) FROM incident_cluster_members l
--     JOIN incident_clusters a ON a.id = l.cluster_id
--     JOIN entries e ON e.id = l.entry_id WHERE e.space_id != a.space_id
--   UNION ALL
--   SELECT 'hierarchy_entries', COUNT(*) FROM hierarchy_entries l
--     JOIN hierarchy_nodes a ON a.id = l.node_id
--     JOIN entries e ON e.id = l.entry_id WHERE e.space_id != a.space_id
--   UNION ALL
--   SELECT 'use_case_entries', COUNT(*) FROM use_case_entries l
--     JOIN use_cases a ON a.id = l.use_case_id
--     JOIN entries e ON e.id = l.entry_id WHERE e.space_id != a.space_id;

ALTER TABLE situations        ADD COLUMN space_id TEXT NOT NULL DEFAULT 'internal';
ALTER TABLE incident_clusters ADD COLUMN space_id TEXT NOT NULL DEFAULT 'internal';
ALTER TABLE hierarchy_nodes   ADD COLUMN space_id TEXT NOT NULL DEFAULT 'internal';
ALTER TABLE use_cases         ADD COLUMN space_id TEXT NOT NULL DEFAULT 'internal';
ALTER TABLE attachments       ADD COLUMN space_id TEXT NOT NULL DEFAULT 'internal';

CREATE INDEX idx_situations_space        ON situations(space_id);
CREATE INDEX idx_incident_clusters_space ON incident_clusters(space_id);
CREATE INDEX idx_hierarchy_nodes_space   ON hierarchy_nodes(space_id);
CREATE INDEX idx_use_cases_space         ON use_cases(space_id);
CREATE INDEX idx_attachments_space       ON attachments(space_id);
