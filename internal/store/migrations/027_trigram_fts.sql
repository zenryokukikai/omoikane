-- migration: 027_trigram_fts
-- Japanese recall was structurally broken: unicode61 does not segment
-- CJK, so kana/kanji runs index as one giant token and partial-word
-- queries (「ステートレス」「アクティブ」…) can never match. Rebuild
-- the two user-facing FTS tables with the trigram tokenizer —
-- substring matching that works for Japanese and English alike.
-- Tokens shorter than 3 chars are handled by a LIKE fallback in
-- store/search.go (trigram cannot index them).

-- ---- entries_fts ----
DROP TRIGGER IF EXISTS entries_ai;
DROP TRIGGER IF EXISTS entries_ad;
DROP TRIGGER IF EXISTS entries_au;
DROP TABLE IF EXISTS entries_fts;

CREATE VIRTUAL TABLE entries_fts USING fts5(
    id UNINDEXED,
    title,
    symptom,
    root_cause,
    resolution,
    attempted_approaches,
    observed_behavior,
    hypotheses,
    body,
    content='entries',
    content_rowid='rowid',
    tokenize="trigram"
);

CREATE TRIGGER entries_ai AFTER INSERT ON entries BEGIN
    INSERT INTO entries_fts(rowid, id, title, symptom, root_cause, resolution,
                            attempted_approaches, observed_behavior, hypotheses, body)
    VALUES (new.rowid, new.id, new.title, new.symptom, new.root_cause, new.resolution,
            new.attempted_approaches, new.observed_behavior, new.hypotheses, new.body);
END;

CREATE TRIGGER entries_ad AFTER DELETE ON entries BEGIN
    INSERT INTO entries_fts(entries_fts, rowid, id, title, symptom, root_cause, resolution,
                            attempted_approaches, observed_behavior, hypotheses, body)
    VALUES ('delete', old.rowid, old.id, old.title, old.symptom, old.root_cause, old.resolution,
            old.attempted_approaches, old.observed_behavior, old.hypotheses, old.body);
END;

CREATE TRIGGER entries_au AFTER UPDATE ON entries BEGIN
    INSERT INTO entries_fts(entries_fts, rowid, id, title, symptom, root_cause, resolution,
                            attempted_approaches, observed_behavior, hypotheses, body)
    VALUES ('delete', old.rowid, old.id, old.title, old.symptom, old.root_cause, old.resolution,
            old.attempted_approaches, old.observed_behavior, old.hypotheses, old.body);
    INSERT INTO entries_fts(rowid, id, title, symptom, root_cause, resolution,
                            attempted_approaches, observed_behavior, hypotheses, body)
    VALUES (new.rowid, new.id, new.title, new.symptom, new.root_cause, new.resolution,
            new.attempted_approaches, new.observed_behavior, new.hypotheses, new.body);
END;

INSERT INTO entries_fts(entries_fts) VALUES ('rebuild');

-- ---- librarian_chat_fts ----
DROP TRIGGER IF EXISTS librarian_chat_ai;
DROP TRIGGER IF EXISTS librarian_chat_ad;
DROP TRIGGER IF EXISTS librarian_chat_au;
DROP TABLE IF EXISTS librarian_chat_fts;

CREATE VIRTUAL TABLE librarian_chat_fts USING fts5(
    content,
    content='librarian_chat',
    content_rowid='rowid',
    tokenize="trigram"
);

CREATE TRIGGER librarian_chat_ai AFTER INSERT ON librarian_chat BEGIN
    INSERT INTO librarian_chat_fts(rowid, content) VALUES (new.rowid, new.content);
END;
CREATE TRIGGER librarian_chat_ad AFTER DELETE ON librarian_chat BEGIN
    INSERT INTO librarian_chat_fts(librarian_chat_fts, rowid, content)
        VALUES('delete', old.rowid, old.content);
END;
CREATE TRIGGER librarian_chat_au AFTER UPDATE ON librarian_chat BEGIN
    INSERT INTO librarian_chat_fts(librarian_chat_fts, rowid, content)
        VALUES('delete', old.rowid, old.content);
    INSERT INTO librarian_chat_fts(rowid, content) VALUES (new.rowid, new.content);
END;

INSERT INTO librarian_chat_fts(librarian_chat_fts) VALUES ('rebuild');
