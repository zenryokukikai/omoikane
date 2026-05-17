-- migration: 015_chat_fts
--
-- Chat messages are NOT indexed for default lookup_* searches —
-- that boundary stays. But entries-only search is too narrow when a
-- user is hunting for "where did we discuss this?"; an opt-in
-- `include_chat: true` on /v1/search needs an FTS index to back it.
--
-- The librarian_chat_fts virtual table mirrors only `content`. Per-
-- message metadata (author, thread, timestamp) is fetched by joining
-- back to librarian_chat via the rowid linkage maintained by the
-- triggers below.
--
-- Why a separate FTS table from entries_fts:
--   - different "documents" (entries vs messages) deserve separate
--     ranking universes. Joining them in one FTS table would let
--     prolific chat threads drown out durable entries on common
--     queries.
--   - the API hands them back as separate result lists, so the
--     storage shape matches the wire shape.

CREATE VIRTUAL TABLE IF NOT EXISTS librarian_chat_fts USING fts5(
    content,
    content='librarian_chat',
    content_rowid='rowid'
);

-- Keep-in-sync triggers. SQLite FTS5 with content= external tables
-- still needs triggers to populate the FTS index; the `content=`
-- option just says "this FTS table doesn't store its own copy of
-- the source — fetch from librarian_chat by rowid". The triggers
-- are the standard pattern from the SQLite FTS5 docs.
CREATE TRIGGER IF NOT EXISTS librarian_chat_ai AFTER INSERT ON librarian_chat BEGIN
    INSERT INTO librarian_chat_fts(rowid, content) VALUES (new.rowid, new.content);
END;
CREATE TRIGGER IF NOT EXISTS librarian_chat_ad AFTER DELETE ON librarian_chat BEGIN
    INSERT INTO librarian_chat_fts(librarian_chat_fts, rowid, content)
        VALUES('delete', old.rowid, old.content);
END;
CREATE TRIGGER IF NOT EXISTS librarian_chat_au AFTER UPDATE ON librarian_chat BEGIN
    INSERT INTO librarian_chat_fts(librarian_chat_fts, rowid, content)
        VALUES('delete', old.rowid, old.content);
    INSERT INTO librarian_chat_fts(rowid, content) VALUES (new.rowid, new.content);
END;

-- Backfill: index every message that existed before this migration ran.
INSERT INTO librarian_chat_fts(rowid, content)
    SELECT rowid, content FROM librarian_chat;
