ALTER TABLE users ADD COLUMN isNewsletterOnly BOOLEAN NOT NULL DEFAULT 0 CHECK(isNewsletterOnly IN (0,1));

CREATE TABLE newsletter_sequence (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), value INTEGER NOT NULL CHECK(value>=0)
) STRICT;
INSERT INTO newsletter_sequence VALUES(1,0);

CREATE TABLE newsletter_requests (
 order_id INTEGER PRIMARY KEY, requested_unix INTEGER NOT NULL,
 expires_unix INTEGER NOT NULL, payload TEXT NOT NULL
) STRICT;

CREATE TABLE newsletter_subscriptions (
 user_id INTEGER PRIMARY KEY REFERENCES users(user_id) ON DELETE CASCADE,
 email_key TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('pending','confirmed','unsubscribed')),
 locale TEXT NOT NULL CHECK(locale IN ('en','fr-BE','nl-BE')),
 consent_version TEXT NOT NULL, source TEXT NOT NULL,
 requested_unix INTEGER NOT NULL, confirmed_unix INTEGER, withdrawn_unix INTEGER,
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
 proved_order INTEGER NOT NULL DEFAULT 0 CHECK(proved_order>=0)
) STRICT;

CREATE TABLE newsletter_proofs (
 hash TEXT PRIMARY KEY NOT NULL CHECK(length(hash)=64),
 user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
 purpose TEXT NOT NULL CHECK(purpose IN ('confirm','unsubscribe')),
 email_key TEXT NOT NULL, revision INTEGER NOT NULL,
 request_order INTEGER NOT NULL, created_unix INTEGER NOT NULL,
 expires_unix INTEGER, consumed_unix INTEGER,
 proposal TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX newsletter_proof_user ON newsletter_proofs(user_id,purpose,revision);

CREATE TABLE newsletter_erasures (
 address_tag TEXT PRIMARY KEY NOT NULL CHECK(length(address_tag)=64),
 through_order INTEGER NOT NULL CHECK(through_order>=0)
) STRICT;
