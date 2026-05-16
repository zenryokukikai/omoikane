-- migration: 014_attachments
--
-- File / image attachments as a first-class resource. Entries and
-- chat messages reference attachments by id in their markdown body
-- (`attached:a-xxxxxx`). The same attachment can be referenced from
-- multiple places — there is no parent FK on the attachment itself,
-- only a project scope. Reference-tracking (which entry/message
-- mentions which attachment) is a separate concern handled at render
-- time by parsing body markdown.
--
-- See spec X-YCXLOW for the design rationale (caption+role
-- mandatory, immutable, agent-readable). The deliberately tight
-- schema enforces caption/role at the DB level so a client can never
-- accidentally upload an opaque blob.
--
-- Storage path is a relative path under the kb-server's /data
-- volume (e.g. attachments/ab/cd/abcd1234...bin). Hash-based fanout
-- keeps any one directory under a few thousand entries even at
-- hundreds-of-thousands-of-files scale.

CREATE TABLE IF NOT EXISTS attachments (
    id            TEXT    PRIMARY KEY,                  -- "a-<8 hex>"
    project_id    TEXT    NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    mime          TEXT    NOT NULL,                     -- "image/png", "video/mp4", ...
    filename      TEXT,                                 -- original filename if supplied; NULL if streamed without one
    size_bytes    INTEGER NOT NULL,
    hash          TEXT    NOT NULL,                     -- sha256 hex of content
    role          TEXT    NOT NULL,                     -- one of the standard vocab (validated app-side)
    caption       TEXT    NOT NULL,                     -- agent-readable description; empty string is allowed at DB level but rejected app-side
    uploaded_by   TEXT    NOT NULL REFERENCES users(id),
    uploaded_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    storage_path  TEXT    NOT NULL                      -- relative to the kb-server /data root
);

CREATE INDEX IF NOT EXISTS idx_attachments_project    ON attachments(project_id);
CREATE INDEX IF NOT EXISTS idx_attachments_hash       ON attachments(hash);
CREATE INDEX IF NOT EXISTS idx_attachments_uploader   ON attachments(uploaded_by);
CREATE INDEX IF NOT EXISTS idx_attachments_role       ON attachments(role);
