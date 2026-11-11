CREATE UNIQUE INDEX account_incarnation_pair ON account_incarnations(user_id,incarnation);

CREATE TABLE personal_recipes (
    id TEXT PRIMARY KEY NOT NULL CHECK(length(id)=32),
    user_id INTEGER NOT NULL,
    incarnation TEXT NOT NULL CHECK(length(incarnation)=32),
    current_version TEXT NOT NULL CHECK(length(current_version)=32),
    title TEXT NOT NULL,
    search_title TEXT NOT NULL,
    source_recipe_id INTEGER,
    source_yield TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(source_yield)),
    created_ns INTEGER NOT NULL,
    updated_ns INTEGER NOT NULL,
    FOREIGN KEY(user_id,incarnation) REFERENCES account_incarnations(user_id,incarnation) ON DELETE CASCADE,
    FOREIGN KEY(id,current_version) REFERENCES personal_recipe_versions(recipe_id,id) DEFERRABLE INITIALLY DEFERRED
) STRICT;
CREATE INDEX personal_recipes_page ON personal_recipes(user_id,updated_ns DESC,id DESC);
CREATE UNIQUE INDEX personal_recipe_copy ON personal_recipes(user_id,incarnation,source_recipe_id) WHERE source_recipe_id IS NOT NULL;

CREATE TABLE personal_recipe_versions (
    id TEXT PRIMARY KEY NOT NULL CHECK(length(id)=32),
    recipe_id TEXT NOT NULL REFERENCES personal_recipes(id) ON DELETE CASCADE,
    previous_id TEXT,
    body TEXT NOT NULL CHECK(json_valid(body)),
    created_ns INTEGER NOT NULL,
    UNIQUE(recipe_id,id),
    FOREIGN KEY(recipe_id,previous_id) REFERENCES personal_recipe_versions(recipe_id,id) DEFERRABLE INITIALLY DEFERRED
) STRICT;
CREATE TRIGGER personal_recipe_version_immutable BEFORE UPDATE ON personal_recipe_versions
BEGIN
    SELECT RAISE(ABORT, 'personal recipe version is immutable');
END;

CREATE TABLE personal_recipe_operations (
    user_id INTEGER NOT NULL,
    incarnation TEXT NOT NULL CHECK(length(incarnation)=32),
    request_key TEXT NOT NULL,
    request_hash TEXT NOT NULL CHECK(length(request_hash)=64),
    action TEXT NOT NULL CHECK(action IN ('save','delete')),
    recipe_id TEXT NOT NULL CHECK(length(recipe_id)=32),
    version_id TEXT NOT NULL CHECK(length(version_id)=32),
    state TEXT NOT NULL CHECK(state IN ('active','deleted')),
    created_ns INTEGER NOT NULL,
    PRIMARY KEY(user_id,incarnation,request_key),
    FOREIGN KEY(user_id,incarnation) REFERENCES account_incarnations(user_id,incarnation) ON DELETE CASCADE
) STRICT;
CREATE INDEX personal_recipe_operation_source ON personal_recipe_operations(user_id,recipe_id);
