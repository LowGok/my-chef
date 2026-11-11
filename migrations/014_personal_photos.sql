CREATE TABLE personal_photo_assets (
    id TEXT PRIMARY KEY NOT NULL CHECK(length(id)=32),
    user_id INTEGER NOT NULL,
    incarnation TEXT NOT NULL CHECK(length(incarnation)=32),
    target TEXT NOT NULL CHECK(length(target)=39),
    binding TEXT NOT NULL,
    delivery_type TEXT NOT NULL CHECK(delivery_type='authenticated'),
    digest TEXT NOT NULL CHECK(length(digest)=64),
    width INTEGER NOT NULL CHECK(width BETWEEN 1 AND 2048),
    height INTEGER NOT NULL CHECK(height BETWEEN 1 AND 2048),
    size INTEGER NOT NULL CHECK(size BETWEEN 1 AND 2097152),
    created_ns INTEGER NOT NULL,
    FOREIGN KEY(user_id,incarnation) REFERENCES account_incarnations(user_id,incarnation) ON DELETE CASCADE,
    UNIQUE(binding,delivery_type,target)
) STRICT;
ALTER TABLE personal_recipe_versions ADD COLUMN photo_id TEXT REFERENCES personal_photo_assets(id);
ALTER TABLE personal_recipe_versions ADD COLUMN image_url TEXT;
ALTER TABLE personal_recipe_versions ADD COLUMN image_public_id TEXT;
ALTER TABLE personal_recipe_versions ADD COLUMN image_binding TEXT;
CREATE TABLE personal_photo_attempts (
    user_id INTEGER NOT NULL,
    incarnation TEXT NOT NULL CHECK(length(incarnation)=32),
    request_key TEXT NOT NULL CHECK(length(request_key)=32),
    request_hash TEXT NOT NULL CHECK(length(request_hash)=64),
    asset_id TEXT NOT NULL CHECK(length(asset_id)=32),
    work_id TEXT NOT NULL CHECK(length(work_id)=32),
    state TEXT NOT NULL CHECK(state IN ('reserved','uploading','attached','failed')),
    lease_ns INTEGER NOT NULL,
    created_ns INTEGER NOT NULL,
    PRIMARY KEY(user_id,incarnation,request_key),
    FOREIGN KEY(user_id,incarnation) REFERENCES account_incarnations(user_id,incarnation) ON DELETE CASCADE
) STRICT;
CREATE INDEX personal_photo_attempt_leases ON personal_photo_attempts(state,lease_ns);
CREATE TABLE personal_photo_requests (
    user_id INTEGER NOT NULL,
    incarnation TEXT NOT NULL CHECK(length(incarnation)=32),
    request_key TEXT NOT NULL CHECK(length(request_key)=32),
    request_hash TEXT NOT NULL CHECK(length(request_hash)=64),
    content_hash TEXT NOT NULL CHECK(length(content_hash)=64),
    state TEXT NOT NULL CHECK(state IN ('processing','reserved','failed')),
    lease_ns INTEGER NOT NULL,
    PRIMARY KEY(user_id,incarnation,request_key),
    FOREIGN KEY(user_id,incarnation) REFERENCES account_incarnations(user_id,incarnation) ON DELETE CASCADE
) STRICT;
CREATE TABLE personal_photo_limits(key TEXT PRIMARY KEY NOT NULL, started_ns INTEGER NOT NULL, count INTEGER NOT NULL) STRICT;
CREATE TABLE personal_photo_limit_key(singleton INTEGER PRIMARY KEY CHECK(singleton=1), key TEXT NOT NULL CHECK(length(key)=64)) STRICT;
INSERT INTO personal_photo_limit_key VALUES(1,lower(hex(randomblob(32))));
