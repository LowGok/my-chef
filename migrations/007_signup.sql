CREATE TABLE signup_requests (
    id TEXT PRIMARY KEY NOT NULL,
    requested INTEGER NOT NULL,
    expires INTEGER NOT NULL,
    draft TEXT NOT NULL
);
CREATE INDEX signup_requests_order ON signup_requests(requested, id);
CREATE INDEX signup_requests_expiry ON signup_requests(expires);

CREATE TABLE signup_proofs (
    id TEXT PRIMARY KEY NOT NULL,
    hash TEXT NOT NULL UNIQUE,
    address_tag TEXT NOT NULL,
    expires INTEGER NOT NULL,
    consumed INTEGER,
    draft TEXT NOT NULL
);
CREATE INDEX signup_proofs_address ON signup_proofs(address_tag, consumed);
CREATE INDEX signup_proofs_expiry ON signup_proofs(expires);
