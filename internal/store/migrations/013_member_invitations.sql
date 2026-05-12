-- migration: 013_member_invitations
--
-- Lets admins invite other humans to join this omoikane instance with
-- a pre-assigned role. Mirrors agent_invitations (migration 011) but
-- with two extra columns:
--
--   target_email — the only email permitted to redeem this code. Set
--                  at issue time and matched against the OAuth identity
--                  at redemption. Required (we agreed: human invites
--                  are addressed to specific people, not "anyone with
--                  the URL"); makes the code safe to share through
--                  channels where it might be observed in transit.
--   target_role  — what role the new user lands in. 'admin' | 'member'.
--                  Set at issue time so a member can't bootstrap
--                  themselves to admin by intercepting an invite.
--
-- Redemption happens inside the OAuth callback: when a NEW google
-- identity (no users row) signs in, we look up a pending invitation
-- by lowercased email. If found, we create the users row with the
-- prescribed role and mark the invitation used. If none, the existing
-- allow-list gate (KB_AUTH_ALLOW_EMAILS / _DOMAINS) decides.

CREATE TABLE IF NOT EXISTS member_invitations (
    code             TEXT PRIMARY KEY,
    inviter_user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    target_email     TEXT NOT NULL,
    target_role      TEXT NOT NULL DEFAULT 'member',  -- 'admin' | 'member'
    note             TEXT,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at       TIMESTAMP NOT NULL,
    used_at          TIMESTAMP,
    used_by_user     TEXT REFERENCES users(id)
);
CREATE INDEX IF NOT EXISTS idx_member_invites_inviter ON member_invitations(inviter_user_id);
CREATE INDEX IF NOT EXISTS idx_member_invites_email   ON member_invitations(target_email);
CREATE INDEX IF NOT EXISTS idx_member_invites_used    ON member_invitations(used_at);
