-- Webhook subscriptions (issue #33): push omoikane events to external
-- agent runtimes. Delivery is at-most-once — consumers reconcile via
-- the list APIs (/v1/comments/recent etc.); this is a latency
-- optimisation, mirroring the SSE stream's contract.
CREATE TABLE IF NOT EXISTS webhook_subscriptions (
    id          TEXT PRIMARY KEY,
    url         TEXT NOT NULL,
    event_types TEXT NOT NULL,             -- JSON array of event type strings
    secret      TEXT NOT NULL,             -- HMAC-SHA256 key (shown once at creation)
    active      INTEGER NOT NULL DEFAULT 1,
    created_by  TEXT NOT NULL REFERENCES users(id),
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
