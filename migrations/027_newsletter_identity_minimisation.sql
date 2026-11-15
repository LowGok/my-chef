ALTER TABLE users ADD COLUMN newsletter_minimised INTEGER NOT NULL DEFAULT 0 CHECK(newsletter_minimised IN (0,1));
ALTER TABLE users ALTER email DROP NOT NULL;
ALTER TABLE users ALTER username DROP NOT NULL;

DROP TRIGGER users_email_key_insert;
DROP TRIGGER users_email_key_update;
CREATE TRIGGER users_email_key_insert BEFORE INSERT ON users
WHEN NOT (
 (NEW.newsletter_minimised=0 AND NEW.email IS NOT NULL AND trim(NEW.email)<>'' AND NEW.email_key IS NOT NULL AND trim(NEW.email_key)<>'' AND NEW.username IS NOT NULL AND trim(NEW.username)<>'')
 OR
 (NEW.newsletter_minimised=1 AND NEW.isNewsletterOnly=1 AND NEW.email IS NULL AND NEW.email_key IS NULL AND NEW.username IS NULL
  AND NEW.password='' AND NEW.firstname='' AND NEW.lastname='' AND NEW.email_verified_at IS NULL AND NEW.email_changed_at IS NULL
  AND NEW.sex IS NULL AND NEW.birthdate IS NULL AND NEW.allergens IS NULL AND NEW.diet IS NULL AND NEW.dislikes IS NULL
  AND NEW.lastmeal IS NULL AND NEW.cooking_efforts IS NULL AND NEW.cooking_technique IS NULL AND NEW.preferences_json IS NULL
  AND (NEW.haspref IS NULL OR NEW.haspref=0))
)
BEGIN SELECT RAISE(ABORT,'invalid newsletter identity state'); END;
CREATE TRIGGER users_email_key_update BEFORE UPDATE ON users
WHEN NOT (
 (NEW.newsletter_minimised=0 AND NEW.email IS NOT NULL AND trim(NEW.email)<>'' AND NEW.email_key IS NOT NULL AND trim(NEW.email_key)<>'' AND NEW.username IS NOT NULL AND trim(NEW.username)<>'')
 OR
 (NEW.newsletter_minimised=1 AND NEW.isNewsletterOnly=1 AND NEW.email IS NULL AND NEW.email_key IS NULL AND NEW.username IS NULL
  AND NEW.password='' AND NEW.firstname='' AND NEW.lastname='' AND NEW.email_verified_at IS NULL AND NEW.email_changed_at IS NULL
  AND NEW.sex IS NULL AND NEW.birthdate IS NULL AND NEW.allergens IS NULL AND NEW.diet IS NULL AND NEW.dislikes IS NULL
  AND NEW.lastmeal IS NULL AND NEW.cooking_efforts IS NULL AND NEW.cooking_technique IS NULL AND NEW.preferences_json IS NULL
  AND (NEW.haspref IS NULL OR NEW.haspref=0))
)
BEGIN SELECT RAISE(ABORT,'invalid newsletter identity state'); END;

CREATE TABLE newsletter_identity_namespace (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 key_version INTEGER NOT NULL CHECK(key_version=1),
 key_id TEXT NOT NULL CHECK(length(key_id)=64 AND key_id NOT GLOB '*[^0-9a-f]*'),
 physical_cleanup_pending INTEGER NOT NULL DEFAULT 0 CHECK(physical_cleanup_pending IN (0,1)),
 legacy_vacuum_pending INTEGER NOT NULL DEFAULT 1 CHECK(legacy_vacuum_pending IN (0,1))
) STRICT;
CREATE UNIQUE INDEX account_incarnation_identity_owner ON account_incarnations(user_id,incarnation);
CREATE TABLE newsletter_identity_matches (
 user_id INTEGER PRIMARY KEY,
 incarnation TEXT NOT NULL CHECK(length(incarnation)=32 AND incarnation NOT GLOB '*[^0-9a-f]*'),
 key_version INTEGER NOT NULL CHECK(key_version=1),
 address_tag TEXT NOT NULL CHECK(length(address_tag)=64 AND address_tag NOT GLOB '*[^0-9a-f]*'),
 UNIQUE(key_version,address_tag),
 FOREIGN KEY(user_id,incarnation) REFERENCES account_incarnations(user_id,incarnation) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED
) STRICT;

-- Reconstruct a known reduced identity inside one transaction: its deferred
-- match supplies the current journal's incarnation before the users insert.
-- Ordinary inserts keep the existing random incarnation behavior.
DROP TRIGGER account_incarnation_insert;
CREATE TRIGGER account_incarnation_insert AFTER INSERT ON users
BEGIN
 INSERT INTO account_incarnations VALUES(NEW.user_id,
  COALESCE((SELECT incarnation FROM newsletter_identity_matches WHERE user_id=NEW.user_id AND NEW.newsletter_minimised=1),lower(hex(randomblob(16)))));
END;

ALTER TABLE newsletter_subscriptions ALTER email_key DROP NOT NULL;
ALTER TABLE newsletter_subscriptions ALTER locale DROP NOT NULL;
ALTER TABLE newsletter_subscriptions ALTER source DROP NOT NULL;
ALTER TABLE newsletter_subscriptions ALTER requested_unix DROP NOT NULL;
CREATE TRIGGER newsletter_subscription_identity_insert BEFORE INSERT ON newsletter_subscriptions
WHEN NOT (
 (NEW.email_key IS NOT NULL AND NEW.email_key<>'' AND NEW.locale IS NOT NULL AND NEW.source IS NOT NULL AND NEW.requested_unix IS NOT NULL)
 OR (NEW.email_key IS NULL AND NEW.locale IS NULL AND NEW.source IS NULL AND NEW.requested_unix IS NULL
  AND EXISTS(SELECT 1 FROM users WHERE user_id=NEW.user_id AND newsletter_minimised=1))
)
BEGIN SELECT RAISE(ABORT,'invalid newsletter subscription identity'); END;
CREATE TRIGGER newsletter_subscription_identity_update BEFORE UPDATE ON newsletter_subscriptions
WHEN NOT (
 (NEW.email_key IS NOT NULL AND NEW.email_key<>'' AND NEW.locale IS NOT NULL AND NEW.source IS NOT NULL AND NEW.requested_unix IS NOT NULL)
 OR (NEW.email_key IS NULL AND NEW.locale IS NULL AND NEW.source IS NULL AND NEW.requested_unix IS NULL
  AND EXISTS(SELECT 1 FROM users WHERE user_id=NEW.user_id AND newsletter_minimised=1))
)
BEGIN SELECT RAISE(ABORT,'invalid newsletter subscription identity'); END;

ALTER TABLE newsletter_proofs ALTER email_key DROP NOT NULL;
ALTER TABLE newsletter_proofs ADD COLUMN identity_incarnation TEXT CHECK(identity_incarnation IS NULL OR (length(identity_incarnation)=32 AND identity_incarnation NOT GLOB '*[^0-9a-f]*'));
ALTER TABLE newsletter_proofs ADD COLUMN identity_key_version INTEGER CHECK(identity_key_version IS NULL OR identity_key_version=1);
ALTER TABLE newsletter_proofs ADD COLUMN identity_address_tag TEXT CHECK(identity_address_tag IS NULL OR (length(identity_address_tag)=64 AND identity_address_tag NOT GLOB '*[^0-9a-f]*'));
CREATE TRIGGER newsletter_proof_identity_insert BEFORE INSERT ON newsletter_proofs
WHEN NOT (
 ((NEW.email_key IS NOT NULL AND NEW.email_key<>'') OR (NEW.email_key IS NULL AND NEW.purpose='unsubscribe' AND NEW.identity_incarnation IS NOT NULL))
 AND ((NEW.identity_incarnation IS NULL AND NEW.identity_key_version IS NULL AND NEW.identity_address_tag IS NULL)
 OR (NEW.identity_incarnation IS NOT NULL AND NEW.identity_key_version IS NOT NULL AND NEW.identity_address_tag IS NOT NULL
  AND EXISTS(SELECT 1 FROM account_incarnations WHERE user_id=NEW.user_id AND incarnation=NEW.identity_incarnation)))
)
BEGIN SELECT RAISE(ABORT,'invalid newsletter proof identity'); END;
CREATE TRIGGER newsletter_proof_identity_update BEFORE UPDATE ON newsletter_proofs
WHEN NOT (
 ((NEW.email_key IS NOT NULL AND NEW.email_key<>'') OR (NEW.email_key IS NULL AND NEW.purpose='unsubscribe' AND NEW.identity_incarnation IS NOT NULL))
 AND ((NEW.identity_incarnation IS NULL AND NEW.identity_key_version IS NULL AND NEW.identity_address_tag IS NULL)
 OR (NEW.identity_incarnation IS NOT NULL AND NEW.identity_key_version IS NOT NULL AND NEW.identity_address_tag IS NOT NULL
  AND EXISTS(SELECT 1 FROM account_incarnations WHERE user_id=NEW.user_id AND incarnation=NEW.identity_incarnation)))
)
BEGIN SELECT RAISE(ABORT,'invalid newsletter proof identity'); END;
