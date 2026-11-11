CREATE TABLE recipe_import_operations (
    id TEXT PRIMARY KEY NOT NULL CHECK(length(id)=32),
    user_id INTEGER NOT NULL,
    incarnation TEXT NOT NULL CHECK(length(incarnation)=32),
    request_key TEXT NOT NULL CHECK(length(request_key)=32),
    request_hash TEXT NOT NULL CHECK(length(request_hash)=64),
    source_kind TEXT NOT NULL CHECK(source_kind IN ('url','photos')),
    source_fingerprint TEXT NOT NULL CHECK(length(source_fingerprint)=64),
    stage TEXT NOT NULL CHECK(stage IN ('checking','selection','awaiting-ai','reading','ready','saved','failed','cancelled','expired')),
    code TEXT NOT NULL DEFAULT '' CHECK(code IN ('','unavailable','source-unavailable','invalid-source','interrupted','cancelled','expired')),
    provider TEXT NOT NULL DEFAULT '' CHECK(provider IN ('','gemini','fixture')),
    model TEXT NOT NULL DEFAULT '' CHECK(length(model)<=128),
    ai_attempts INTEGER NOT NULL DEFAULT 0 CHECK(ai_attempts IN (0,1)),
    lease_hash TEXT NOT NULL DEFAULT '' CHECK(length(lease_hash) IN (0,64)),
    cipher BLOB,
    recipe_id TEXT NOT NULL DEFAULT '' CHECK(length(recipe_id) IN (0,32)),
    version_id TEXT NOT NULL DEFAULT '' CHECK(length(version_id) IN (0,32)),
    created_ns INTEGER NOT NULL,
    updated_ns INTEGER NOT NULL,
    expires_ns INTEGER NOT NULL CHECK(expires_ns>created_ns),
    UNIQUE(user_id,incarnation,request_key),
    FOREIGN KEY(user_id,incarnation) REFERENCES account_incarnations(user_id,incarnation) ON DELETE CASCADE
) STRICT;
CREATE INDEX recipe_import_owner ON recipe_import_operations(user_id,incarnation,stage);
CREATE INDEX recipe_import_expiry ON recipe_import_operations(expires_ns);

CREATE TABLE personal_recipe_import_sources (
    version_id TEXT PRIMARY KEY NOT NULL REFERENCES personal_recipe_versions(id) ON DELETE CASCADE,
    source_kind TEXT NOT NULL CHECK(source_kind IN ('url','photos')),
    source_url TEXT NOT NULL DEFAULT '',
    source_title TEXT NOT NULL DEFAULT '',
    imported_ns INTEGER NOT NULL
) STRICT;
CREATE TRIGGER personal_recipe_import_source_immutable BEFORE UPDATE ON personal_recipe_import_sources
BEGIN
    SELECT RAISE(ABORT, 'personal recipe import source is immutable');
END;
