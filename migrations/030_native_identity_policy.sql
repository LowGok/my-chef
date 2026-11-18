ALTER TABLE native_families ADD COLUMN identity_revision INTEGER NOT NULL DEFAULT 0 CHECK(identity_revision >= 0);

CREATE TABLE native_identity_policy (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    revision INTEGER NOT NULL CHECK(revision >= 0),
    active INTEGER NOT NULL CHECK(active IN (0,1)),
    sync_until INTEGER NOT NULL CHECK(sync_until >= 0),
    sync_revision INTEGER NOT NULL CHECK(sync_revision >= 0),
    CHECK(active = 0 OR sync_until = 0)
);
INSERT INTO native_identity_policy VALUES(1,0,0,0,0);
