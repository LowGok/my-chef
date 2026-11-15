CREATE TABLE analytics_preferences (
 incarnation TEXT PRIMARY KEY REFERENCES account_incarnations(incarnation) ON DELETE CASCADE,
 enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
 revision INTEGER NOT NULL DEFAULT 0 CHECK(revision>=0),
 subject TEXT NOT NULL DEFAULT '', epoch TEXT NOT NULL DEFAULT '',
 notice_version TEXT NOT NULL DEFAULT '', notice_hash TEXT NOT NULL DEFAULT '',
 basis_version TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT 'migration_off',
 changed_unix INTEGER NOT NULL, deletion_status TEXT NOT NULL DEFAULT 'none',
 sequence INTEGER NOT NULL DEFAULT 0, capture_gap INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE TABLE analytics_receipts (
 id TEXT PRIMARY KEY, incarnation TEXT NOT NULL REFERENCES account_incarnations(incarnation) ON DELETE CASCADE,
 revision INTEGER NOT NULL, enabled INTEGER NOT NULL, source TEXT NOT NULL,
 notice_version TEXT NOT NULL, basis_version TEXT NOT NULL, changed_unix INTEGER NOT NULL,
 platform TEXT NOT NULL CHECK(platform IN ('web','ios','android')),
 UNIQUE(incarnation,revision)
) STRICT;
CREATE TABLE analytics_outbox (
 id TEXT PRIMARY KEY, incarnation TEXT NOT NULL REFERENCES account_incarnations(incarnation) ON DELETE CASCADE,
 subject TEXT NOT NULL, epoch TEXT NOT NULL, sequence INTEGER NOT NULL,
 occurred_unix INTEGER NOT NULL, name TEXT NOT NULL CHECK(name IN ('preferences_saved','meal_plan_saved','shopping_list_edited','favorite_saved','feedback_submitted','personal_recipe_saved','recipe_import_saved')),
 platform TEXT NOT NULL CHECK(platform IN ('web','ios','android')),
 plan TEXT NOT NULL CHECK(plan IN ('free','premium')),
 receipt TEXT NOT NULL, UNIQUE(incarnation,epoch,receipt,name)
) STRICT;
CREATE INDEX analytics_outbox_delivery ON analytics_outbox(occurred_unix,id);
CREATE TABLE analytics_signup (
 email_key TEXT PRIMARY KEY, enabled INTEGER NOT NULL CHECK(enabled IN(0,1)),
 notice_version TEXT NOT NULL, expires_unix INTEGER NOT NULL
) STRICT;
