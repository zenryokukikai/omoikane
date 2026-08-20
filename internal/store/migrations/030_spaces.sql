-- Spaces / groups read-ACL foundation (issue #60, Phase 1 slice 1).
-- A space is a visibility boundary; project_id stays as the category
-- WITHIN a space (orthogonal — existing semantics unchanged). Groups
-- carry membership; space_acl grants a group access to a space.
--
-- Reserved identifiers (deliberate reserved words, per design v2):
--   space 'internal'   (kind=internal)  — every member of group
--                                         'internal' can read it
--   group 'internal'                    — auto-joined on user creation
--                                         (unless the role is external)
--   space 'p-<user_id>' (kind=personal) — implicit ACL: the owner only,
--                                         no space_acl rows
--
-- Visibility is resolved ONLY by store.VisibleSpaces; the SQL predicate
-- is built ONLY by store.SpaceFilter. No other code composes space
-- conditions by hand.

CREATE TABLE IF NOT EXISTS groups (
    id         TEXT PRIMARY KEY,          -- g-8hex / 'internal'
    name       TEXT NOT NULL UNIQUE,      -- free-form (dept / role / cross-cutting)
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS group_members (
    group_id TEXT NOT NULL REFERENCES groups(id),
    user_id  TEXT NOT NULL,
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE IF NOT EXISTS spaces (
    id         TEXT PRIMARY KEY,          -- sp-8hex / 'internal' / 'p-<user_id>'
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL DEFAULT 'restricted',  -- internal | restricted | personal
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS space_acl (
    space_id TEXT NOT NULL REFERENCES spaces(id),
    group_id TEXT NOT NULL REFERENCES groups(id),
    role     TEXT NOT NULL,               -- 'admin' | 'member'
    PRIMARY KEY (space_id, group_id)
);

-- Every entry lives in exactly one space. Existing rows land in
-- 'internal' (behaviour-preserving migration).
ALTER TABLE entries ADD COLUMN space_id TEXT NOT NULL DEFAULT 'internal';
CREATE INDEX IF NOT EXISTS idx_entries_space ON entries(space_id);

-- Index for the visibility-resolution joins (space_acl PK already
-- covers space_id lookups; group_members PK covers group_id lookups).
CREATE INDEX IF NOT EXISTS idx_group_members_user ON group_members(user_id);
CREATE INDEX IF NOT EXISTS idx_space_acl_group    ON space_acl(group_id);

-- ============================================================
-- Backfill (same migration, behaviour-preserving):
--   1. reserved space + group 'internal'
--   2. every existing user (human AND agent users) joins group
--      'internal'
--   3. every existing user gets a personal space p-<user_id>
-- ============================================================
INSERT INTO spaces (id, name, kind) VALUES ('internal', 'internal', 'internal');
INSERT INTO groups (id, name)       VALUES ('internal', 'internal');

INSERT INTO group_members (group_id, user_id)
    SELECT 'internal', id FROM users;

INSERT INTO spaces (id, name, kind)
    SELECT 'p-' || id, name, 'personal' FROM users;
