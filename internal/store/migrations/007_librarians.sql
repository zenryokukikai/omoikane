-- migration: 007_librarians
-- Phase 5 — Librarian community schema per docs/design.md §4.2.5 + §23.

CREATE TABLE IF NOT EXISTS librarian_instances (
    instance_id    TEXT PRIMARY KEY,
    role           TEXT NOT NULL,             -- coordinator|cataloger|curator|detective|conservator|scout|summarizer|judge
    skill_version  TEXT,
    agent_runtime  TEXT,                      -- 'claude-code' | 'opencode' | 'stub' | ...
    status         TEXT NOT NULL DEFAULT 'OBSERVING',  -- OBSERVING|ACTIVE|PAUSED|STOPPED
    started_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    heartbeat_at   TIMESTAMP,
    metadata       TEXT
);
CREATE INDEX IF NOT EXISTS idx_librarian_role ON librarian_instances(role);
CREATE INDEX IF NOT EXISTS idx_librarian_status ON librarian_instances(status);

CREATE TABLE IF NOT EXISTS chat_threads (
    thread_id     TEXT PRIMARY KEY,
    title         TEXT,
    intent        TEXT,                       -- observation|question|proposal|...
    status        TEXT NOT NULL DEFAULT 'OPEN',  -- OPEN|CLOSED|ESCALATED
    opened_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    closed_at     TIMESTAMP,
    summary       TEXT,
    related_entries TEXT,                     -- JSON array
    metadata      TEXT
);
CREATE INDEX IF NOT EXISTS idx_threads_status ON chat_threads(status);

CREATE TABLE IF NOT EXISTS librarian_chat (
    id            TEXT PRIMARY KEY,
    thread_id     TEXT REFERENCES chat_threads(thread_id) ON DELETE CASCADE,
    timestamp     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    author_role   TEXT NOT NULL,
    author_instance_id TEXT,
    reply_to      TEXT,
    mentions      TEXT,                       -- JSON array
    intent        TEXT,                       -- observation|question|proposal|celebration|concern|arbitration|PASS
    content       TEXT NOT NULL,
    related_entries TEXT,                     -- JSON array
    input_tokens  INTEGER DEFAULT 0,
    output_tokens INTEGER DEFAULT 0,
    metadata      TEXT
);
CREATE INDEX IF NOT EXISTS idx_chat_thread     ON librarian_chat(thread_id);
CREATE INDEX IF NOT EXISTS idx_chat_role       ON librarian_chat(author_role);
CREATE INDEX IF NOT EXISTS idx_chat_timestamp  ON librarian_chat(timestamp);

CREATE TABLE IF NOT EXISTS librarian_tasks (
    task_id       TEXT PRIMARY KEY,
    role          TEXT NOT NULL,
    title         TEXT NOT NULL,
    description   TEXT,
    priority      INTEGER NOT NULL DEFAULT 100,
    status        TEXT NOT NULL DEFAULT 'PENDING',  -- PENDING|IN_PROGRESS|DONE|FAILED|CANCELLED
    assigned_to   TEXT REFERENCES librarian_instances(instance_id),
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at    TIMESTAMP,
    completed_at  TIMESTAMP,
    result        TEXT,
    metadata      TEXT
);
CREATE INDEX IF NOT EXISTS idx_tasks_role     ON librarian_tasks(role);
CREATE INDEX IF NOT EXISTS idx_tasks_status   ON librarian_tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_priority ON librarian_tasks(priority);

CREATE TABLE IF NOT EXISTS quartet_assignments (
    id            TEXT PRIMARY KEY,
    topic         TEXT NOT NULL,
    thread_id     TEXT REFERENCES chat_threads(thread_id),
    participant_1 TEXT NOT NULL,
    participant_2 TEXT NOT NULL,
    participant_3 TEXT NOT NULL,
    judge         TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'OPEN',  -- OPEN|VOTING|DECIDED|ABANDONED
    decision      TEXT,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decided_at    TIMESTAMP,
    metadata      TEXT
);
CREATE INDEX IF NOT EXISTS idx_quartet_status ON quartet_assignments(status);

CREATE TABLE IF NOT EXISTS external_findings (
    id            TEXT PRIMARY KEY,
    agent_lens    TEXT NOT NULL,              -- 'scout' | 'detective' | other roles
    instance_id   TEXT REFERENCES librarian_instances(instance_id),
    source_url    TEXT,
    source_title  TEXT,
    excerpt       TEXT,
    relevance     REAL,
    tags          TEXT,                       -- JSON array
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    metadata      TEXT
);
CREATE INDEX IF NOT EXISTS idx_findings_lens     ON external_findings(agent_lens);
CREATE INDEX IF NOT EXISTS idx_findings_relevance ON external_findings(relevance);

CREATE TABLE IF NOT EXISTS finding_correlations (
    finding_id    TEXT NOT NULL REFERENCES external_findings(id) ON DELETE CASCADE,
    entry_id      TEXT NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    correlation   REAL DEFAULT 1.0,
    PRIMARY KEY (finding_id, entry_id)
);
