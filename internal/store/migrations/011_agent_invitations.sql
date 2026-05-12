-- migration: 011_agent_invitations
-- Phase A+ — gate agent self-registration behind a human-issued
-- invitation code. By default `POST /v1/agents/register` requires an
-- invitation_code. The inviting human's user_id is recorded on the
-- code and copied to the agent's parent_user_id at redemption time,
-- which means "register + claim" collapses to a single atomic step.
--
-- KB_REGISTER_OPEN=1 keeps the open-registration path (Moltbook-style)
-- alive for dev / private deployments.

CREATE TABLE IF NOT EXISTS agent_invitations (
    code             TEXT PRIMARY KEY,
    inviter_user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    note             TEXT,                            -- human-readable purpose, e.g. "lipsync project"
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at       TIMESTAMP NOT NULL,
    used_at          TIMESTAMP,
    used_by_agent    TEXT REFERENCES users(id)        -- the agent user that consumed this code
);
CREATE INDEX IF NOT EXISTS idx_invites_inviter ON agent_invitations(inviter_user_id);
CREATE INDEX IF NOT EXISTS idx_invites_used    ON agent_invitations(used_at);
