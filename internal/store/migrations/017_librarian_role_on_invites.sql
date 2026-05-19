-- migration: 017_librarian_role_on_invites
--
-- Carve out a dedicated permission lane for librarian-side agents.
--
-- Before: every agent registered via /v1/agents/register received the
-- same scopes (read,write). Librarian-side agents need to call
-- /v1/librarian/instances (POST) to register themselves, which is
-- gated by `admin` scope. There was no way to issue a token with
-- "can register a librarian instance, but cannot administer the
-- server" — admin was the only scope that opened that door.
--
-- This migration adds a `librarian_role` on agent_invitations and
-- users. When an admin issues an invite with a librarian_role set,
-- the redeemed agent's user record carries that role AND its api
-- token receives the new `librarian` scope (handled in store/api,
-- not in the SQL). The `/v1/librarian/instances` POST handler then
-- requires `librarian` scope instead of `admin`.
--
-- The role values map 1:1 to the dist/skills/librarians/<role>
-- bundles: coordinator, cataloger, curator, detective, conservator,
-- scout, summarizer, judge. We don't CHECK constraint the value
-- because new librarian roles may be added before a schema migration
-- can ship; the application layer validates against the canonical
-- list.

ALTER TABLE agent_invitations ADD COLUMN librarian_role TEXT;
ALTER TABLE users             ADD COLUMN librarian_role TEXT;

-- Indexed for the dashboard's "show librarian-role invites separately"
-- query and for the harness's "find me users with role X" lookup.
CREATE INDEX IF NOT EXISTS idx_invites_librarian_role
    ON agent_invitations(librarian_role)
    WHERE librarian_role IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_users_librarian_role
    ON users(librarian_role)
    WHERE librarian_role IS NOT NULL;
