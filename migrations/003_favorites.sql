CREATE TABLE favorites (
 user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
 recipe_id INTEGER NOT NULL,
 body TEXT NOT NULL,
 saved BOOLEAN NOT NULL DEFAULT 1 CHECK(saved IN (0,1)),
 created_unix INTEGER NOT NULL,
 PRIMARY KEY(user_id,recipe_id)
);
CREATE TABLE favorite_queue (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
 recipe_id INTEGER NOT NULL,
 week TEXT NOT NULL,
 body TEXT NOT NULL,
 consumed_operation INTEGER REFERENCES generations(id),
 UNIQUE(user_id,week,recipe_id)
);
CREATE INDEX favorite_queue_pending ON favorite_queue(user_id,week,consumed_operation,id);
CREATE TABLE favorite_swaps (
 user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
 request_key TEXT NOT NULL,
 request_hash TEXT NOT NULL,
 body TEXT NOT NULL,
 PRIMARY KEY(user_id,request_key)
);
