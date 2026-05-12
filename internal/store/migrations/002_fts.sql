-- migration: 002_fts
-- FTS5 virtual table mirroring entries' searchable fields.

CREATE VIRTUAL TABLE IF NOT EXISTS entries_fts USING fts5(
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
    content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS entries_ai AFTER INSERT ON entries BEGIN
    INSERT INTO entries_fts(rowid, id, title, symptom, root_cause, resolution,
                            attempted_approaches, observed_behavior, hypotheses, body)
    VALUES (new.rowid, new.id, new.title, new.symptom, new.root_cause, new.resolution,
            new.attempted_approaches, new.observed_behavior, new.hypotheses, new.body);
END;

CREATE TRIGGER IF NOT EXISTS entries_ad AFTER DELETE ON entries BEGIN
    INSERT INTO entries_fts(entries_fts, rowid, id, title, symptom, root_cause, resolution,
                            attempted_approaches, observed_behavior, hypotheses, body)
    VALUES ('delete', old.rowid, old.id, old.title, old.symptom, old.root_cause, old.resolution,
            old.attempted_approaches, old.observed_behavior, old.hypotheses, old.body);
END;

CREATE TRIGGER IF NOT EXISTS entries_au AFTER UPDATE ON entries BEGIN
    INSERT INTO entries_fts(entries_fts, rowid, id, title, symptom, root_cause, resolution,
                            attempted_approaches, observed_behavior, hypotheses, body)
    VALUES ('delete', old.rowid, old.id, old.title, old.symptom, old.root_cause, old.resolution,
            old.attempted_approaches, old.observed_behavior, old.hypotheses, old.body);
    INSERT INTO entries_fts(rowid, id, title, symptom, root_cause, resolution,
                            attempted_approaches, observed_behavior, hypotheses, body)
    VALUES (new.rowid, new.id, new.title, new.symptom, new.root_cause, new.resolution,
            new.attempted_approaches, new.observed_behavior, new.hypotheses, new.body);
END;
