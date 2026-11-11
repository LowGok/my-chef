ALTER TABLE meals ADD COLUMN personal_source TEXT CHECK(personal_source IS NULL OR json_valid(personal_source));
CREATE INDEX meals_personal_photo ON meals(json_extract(personal_source,'$.photoId')) WHERE personal_source IS NOT NULL;
CREATE TABLE personal_recipe_queue (
    id TEXT PRIMARY KEY NOT NULL CHECK(length(id)=32),
    user_id INTEGER NOT NULL,
    incarnation TEXT NOT NULL CHECK(length(incarnation)=32),
    recipe_id TEXT NOT NULL REFERENCES personal_recipes(id) ON DELETE CASCADE,
    version_id TEXT NOT NULL CHECK(length(version_id)=32),
    week TEXT NOT NULL,
    body TEXT NOT NULL CHECK(json_valid(body)),
    consumed_operation INTEGER REFERENCES generations(id),
    FOREIGN KEY(user_id,incarnation) REFERENCES account_incarnations(user_id,incarnation) ON DELETE CASCADE,
    FOREIGN KEY(recipe_id,version_id) REFERENCES personal_recipe_versions(recipe_id,id) ON DELETE CASCADE,
    UNIQUE(user_id,week,recipe_id)
) STRICT;
CREATE INDEX personal_recipe_queue_pending ON personal_recipe_queue(user_id,week,consumed_operation,id);
CREATE TABLE personal_queue_receipts (
    user_id INTEGER NOT NULL,
    incarnation TEXT NOT NULL CHECK(length(incarnation)=32),
    request_key TEXT NOT NULL CHECK(length(request_key)=32),
    request_hash TEXT NOT NULL CHECK(length(request_hash)=64),
    action TEXT NOT NULL CHECK(action IN ('queue','remove')),
    queue_id TEXT NOT NULL CHECK(length(queue_id)=32),
    recipe_id TEXT NOT NULL CHECK(length(recipe_id)=32),
    version_id TEXT NOT NULL CHECK(length(version_id)=32),
    week TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('active','removed','deleted')),
    PRIMARY KEY(user_id,incarnation,request_key),
    FOREIGN KEY(user_id,incarnation) REFERENCES account_incarnations(user_id,incarnation) ON DELETE CASCADE
) STRICT;
