CREATE TABLE native_families (
    id TEXT PRIMARY KEY NOT NULL CHECK(length(id)=32),
    anchor_hash TEXT NOT NULL UNIQUE REFERENCES sessions(hash) ON DELETE CASCADE,
    platform TEXT NOT NULL CHECK(platform IN ('ios','android')),
    expires_unix INTEGER NOT NULL
) STRICT;
CREATE INDEX native_families_expiry ON native_families(expires_unix);

CREATE TABLE native_accesses (
    hash TEXT PRIMARY KEY NOT NULL REFERENCES sessions(hash) ON DELETE CASCADE,
    family_id TEXT NOT NULL REFERENCES native_families(id) ON DELETE CASCADE
) STRICT;
CREATE INDEX native_accesses_family ON native_accesses(family_id);

CREATE TABLE native_refreshes (
    hash TEXT PRIMARY KEY NOT NULL CHECK(length(hash)=64),
    family_id TEXT NOT NULL REFERENCES native_families(id) ON DELETE CASCADE,
    consumed_unix INTEGER
) STRICT;
CREATE INDEX native_refreshes_family ON native_refreshes(family_id);

-- Native access rows are also transaction-time actors for the existing APIs.
-- Deleting an anchor revokes the entire family through foreign-key cascades.
CREATE TRIGGER native_access_cleanup AFTER DELETE ON native_accesses
BEGIN
    DELETE FROM sessions WHERE hash=OLD.hash;
END;
CREATE TRIGGER native_family_cleanup AFTER DELETE ON native_families
BEGIN
    DELETE FROM sessions WHERE hash=OLD.anchor_hash;
END;
