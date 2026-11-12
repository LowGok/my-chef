CREATE TABLE entitlement_fixture (
    singleton INTEGER PRIMARY KEY CHECK(singleton=1),
    generation TEXT NOT NULL UNIQUE CHECK(length(generation)=32),
    lease_id TEXT,
    lease_expires_ns INTEGER,
    ordinary_used INTEGER NOT NULL DEFAULT 0 CHECK(ordinary_used BETWEEN 0 AND 1024),
    CHECK((lease_id IS NULL AND lease_expires_ns IS NULL)
       OR (lease_id IS NOT NULL AND length(lease_id)>0 AND lease_expires_ns IS NOT NULL AND lease_expires_ns>0))
) STRICT;
INSERT INTO entitlement_fixture(singleton,generation) VALUES(1,lower(hex(randomblob(16))));

CREATE TABLE entitlement_grants (
    id TEXT PRIMARY KEY CHECK(length(id)>0),
    incarnation TEXT NOT NULL REFERENCES account_incarnations(incarnation) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK(length(source)>0),
    starts_ns INTEGER NOT NULL,
    ends_ns INTEGER NOT NULL CHECK(ends_ns>starts_ns),
    revoked_ns INTEGER,
    lease_id TEXT,
    generation TEXT,
    CHECK(source<>'fixture' OR (lease_id IS NOT NULL AND length(lease_id)>0
          AND generation IS NOT NULL AND length(generation)=32))
) STRICT;
CREATE INDEX entitlement_grants_owner ON entitlement_grants(incarnation,source,generation,lease_id,ends_ns);

CREATE TRIGGER entitlement_grant_identity BEFORE UPDATE ON entitlement_grants
WHEN OLD.id IS NOT NEW.id OR OLD.incarnation IS NOT NEW.incarnation
  OR OLD.source IS NOT NEW.source OR OLD.starts_ns IS NOT NEW.starts_ns OR OLD.ends_ns IS NOT NEW.ends_ns
  OR OLD.lease_id IS NOT NEW.lease_id OR OLD.generation IS NOT NEW.generation
  OR (OLD.revoked_ns IS NOT NULL AND OLD.revoked_ns IS NOT NEW.revoked_ns)
BEGIN
    SELECT RAISE(ABORT,'entitlement grant facts are immutable');
END;

CREATE TABLE entitlement_fixture_operations (
    generation TEXT NOT NULL,
    incarnation TEXT NOT NULL REFERENCES account_incarnations(incarnation) ON DELETE CASCADE,
    operation_id TEXT NOT NULL CHECK(length(operation_id)=32),
    state TEXT NOT NULL CHECK(state IN ('premium','expired','free')),
    reserved INTEGER NOT NULL CHECK(reserved IN (0,1)),
    PRIMARY KEY(generation,incarnation,operation_id)
) STRICT;
CREATE UNIQUE INDEX entitlement_fixture_reserved ON entitlement_fixture_operations(generation,incarnation) WHERE reserved=1;

CREATE TABLE entitlement_retired_leases (
    id TEXT PRIMARY KEY CHECK(length(id)>0),
    expires_ns INTEGER NOT NULL
) STRICT;
