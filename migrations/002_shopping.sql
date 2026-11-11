CREATE TABLE shopping_lists (
 user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
 week TEXT NOT NULL,
 revision INTEGER NOT NULL,
 body TEXT NOT NULL,
 PRIMARY KEY(user_id, week)
);
