CREATE TABLE recovery_clock (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    sequence INTEGER NOT NULL CHECK (sequence >= 0)
) STRICT;
INSERT INTO recovery_clock (singleton, sequence) VALUES (1, 0);

CREATE TABLE recovery_account_orders (
    user_id INTEGER PRIMARY KEY REFERENCES users(user_id) ON DELETE CASCADE,
    eligibility_order INTEGER NOT NULL DEFAULT 0 CHECK (eligibility_order >= 0),
    latest_issued_order INTEGER NOT NULL DEFAULT 0 CHECK (latest_issued_order >= 0)
) STRICT;
INSERT INTO recovery_account_orders (user_id)
SELECT user_id FROM users WHERE email_verified_at IS NOT NULL;

CREATE TABLE recovery_jobs (
    accepted_order INTEGER PRIMARY KEY CHECK (accepted_order > 0),
    address_tag TEXT NOT NULL CHECK (length(address_tag) = 64),
    expires_ns INTEGER NOT NULL,
    draft TEXT NOT NULL
) STRICT;
CREATE INDEX recovery_jobs_address_order ON recovery_jobs(address_tag, accepted_order);
CREATE INDEX recovery_jobs_expiry ON recovery_jobs(expires_ns, accepted_order);

CREATE TABLE recovery_proofs (
    hash TEXT PRIMARY KEY NOT NULL CHECK (length(hash) = 64),
    accepted_order INTEGER NOT NULL UNIQUE CHECK (accepted_order > 0),
    user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    address_tag TEXT NOT NULL CHECK (length(address_tag) = 64),
    eligibility_order INTEGER NOT NULL CHECK (eligibility_order >= 0 AND eligibility_order < accepted_order),
    issued_ns INTEGER NOT NULL,
    expires_ns INTEGER NOT NULL CHECK (expires_ns > issued_ns),
    consumed_ns INTEGER
) STRICT;
CREATE INDEX recovery_proofs_user ON recovery_proofs(user_id, consumed_ns);
CREATE INDEX recovery_proofs_expiry ON recovery_proofs(expires_ns, accepted_order);

-- Account deletion cascades to the proof. Do not leave its encrypted
-- delivery copy waiting in a queued or in-flight outbox row.
CREATE TRIGGER recovery_proof_delete_erases_mail
AFTER DELETE ON recovery_proofs
BEGIN
    UPDATE email_outbox
    SET state = 'failed',
        reason = 'proof_invalidated',
        data = CASE WHEN json_valid(data) THEN json_remove(data, '$.links') ELSE '{}' END,
        updated_at = unixepoch()
    WHERE key = 'password-recovery/' || OLD.accepted_order
        AND template = 'password-recovery/v1'
        AND state IN ('queued', 'sending');
END;
