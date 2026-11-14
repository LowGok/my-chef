CREATE TABLE analytics_generation (
 operation_id INTEGER PRIMARY KEY REFERENCES generations(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL, platform TEXT NOT NULL, epoch TEXT NOT NULL
) STRICT;
