CREATE TABLE email_outbox (
    id INTEGER PRIMARY KEY,
    key TEXT NOT NULL UNIQUE,
    recipient TEXT NOT NULL,
    template TEXT NOT NULL,
    data TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'queued',
    outcome TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_at INTEGER NOT NULL,
    fingerprint TEXT NOT NULL DEFAULT '',
    provider_id TEXT UNIQUE,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX email_outbox_pending ON email_outbox(state, next_at, id);
CREATE TABLE email_events (
    id TEXT PRIMARY KEY NOT NULL,
    provider_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    occurred_at INTEGER NOT NULL,
    received_at INTEGER NOT NULL
);
CREATE INDEX email_events_provider ON email_events(provider_id);
