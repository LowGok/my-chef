CREATE TABLE email_verification_proofs (
    id TEXT PRIMARY KEY NOT NULL,
    user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    hash TEXT NOT NULL UNIQUE,
    address_tag TEXT NOT NULL,
    expires INTEGER NOT NULL,
    consumed INTEGER
);
CREATE INDEX email_verification_user ON email_verification_proofs(user_id, consumed);
CREATE INDEX email_verification_expiry ON email_verification_proofs(expires);
