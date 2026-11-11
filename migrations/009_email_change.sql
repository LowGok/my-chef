ALTER TABLE users ADD COLUMN email_changed_at INTEGER;

CREATE TABLE pending_email_changes (
    id TEXT PRIMARY KEY NOT NULL,
    user_id INTEGER NOT NULL UNIQUE REFERENCES users(user_id) ON DELETE CASCADE,
    original_email TEXT NOT NULL,
    original_key TEXT NOT NULL,
    original_verified_at INTEGER,
    proposed_email TEXT NOT NULL,
    proposed_key TEXT NOT NULL,
    expires INTEGER NOT NULL,
    current_approved INTEGER NOT NULL DEFAULT 0,
    proposed_approved INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX pending_email_changes_expiry ON pending_email_changes(expires);

CREATE TABLE email_change_proofs (
    hash TEXT PRIMARY KEY NOT NULL,
    change_id TEXT NOT NULL REFERENCES pending_email_changes(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK (purpose IN ('approve', 'confirm', 'cancel')),
    consumed INTEGER,
    UNIQUE (change_id, purpose)
);
