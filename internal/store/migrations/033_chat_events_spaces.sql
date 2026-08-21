-- Chat / events / webhook surfaces join the space read-ACL (issue #60,
-- Phase 1 slice 4). NB: numbered 033 because version 30 was burned in
-- production (see the note atop 031_spaces.sql) — never reuse a version
-- number that ever shipped.
--
-- librarian_tasks.space_id: an open-work claim mints a task titled
-- "impl: <entry title>" — the title reproduces restricted-entry text, so
-- the task must live in the entry's space (stamped at claim time) and
-- the list/claim/complete paths filter on it. Existing rows land in
-- 'internal' via the DEFAULT (all pre-slice-4 tasks derive from
-- internal-space entries — behaviour-preserving).
--
-- webhook_subscriptions.space_scope: NULL keeps the current "deliver
-- everything" behaviour — existing subscriptions (the /talk responder
-- runtime) are trusted infrastructure and must not break. A non-NULL
-- JSON array restricts delivery to events whose space is listed;
-- events without a space are then NOT delivered (fail-closed).

ALTER TABLE librarian_tasks ADD COLUMN space_id TEXT NOT NULL DEFAULT 'internal';
CREATE INDEX idx_librarian_tasks_space ON librarian_tasks(space_id);

ALTER TABLE webhook_subscriptions ADD COLUMN space_scope TEXT; -- NULL = all spaces (trusted infra)
