CREATE TABLE analytics_installation(singleton INTEGER PRIMARY KEY CHECK(singleton=1),required INTEGER NOT NULL DEFAULT 0 CHECK(required IN(0,1)),restore_paused INTEGER NOT NULL DEFAULT 0 CHECK(restore_paused IN(0,1))) STRICT;
INSERT INTO analytics_installation(singleton) VALUES(1);
