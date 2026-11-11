CREATE TABLE lifecycle_binding (
    singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
    binding TEXT NOT NULL UNIQUE CHECK(length(binding) = 32),
    stage TEXT NOT NULL DEFAULT 'new' CHECK(stage IN ('new','prepared','ready'))
) STRICT;
INSERT INTO lifecycle_binding(singleton,binding) VALUES(1, lower(hex(randomblob(16))));

CREATE TABLE account_incarnations (
    user_id INTEGER PRIMARY KEY REFERENCES users(user_id) ON DELETE CASCADE,
    incarnation TEXT NOT NULL UNIQUE CHECK(length(incarnation) = 32)
) STRICT;
INSERT INTO account_incarnations SELECT user_id, lower(hex(randomblob(16))) FROM users;

CREATE TRIGGER account_incarnation_insert AFTER INSERT ON users
BEGIN
    INSERT INTO account_incarnations VALUES(NEW.user_id, lower(hex(randomblob(16))));
END;
CREATE TRIGGER account_incarnation_immutable BEFORE UPDATE ON account_incarnations
BEGIN
    SELECT RAISE(ABORT, 'account incarnation is immutable');
END;
CREATE TRIGGER lifecycle_binding_immutable BEFORE UPDATE OF binding ON lifecycle_binding
BEGIN
    SELECT RAISE(ABORT, 'lifecycle binding is immutable');
END;
CREATE TRIGGER lifecycle_binding_required BEFORE DELETE ON lifecycle_binding
BEGIN
    SELECT RAISE(ABORT, 'lifecycle binding is required');
END;

CREATE TABLE lifecycle_operations (
    operation TEXT PRIMARY KEY NOT NULL CHECK(length(operation) = 32),
    incarnation TEXT NOT NULL UNIQUE CHECK(length(incarnation) = 32),
    created_ns INTEGER NOT NULL,
    notice TEXT NOT NULL DEFAULT 'skipped' CHECK(notice IN ('queued','skipped'))
) STRICT;

CREATE TABLE account_mail_owners (
    outbox_id INTEGER PRIMARY KEY REFERENCES email_outbox(id) ON DELETE CASCADE,
    incarnation TEXT NOT NULL CHECK(length(incarnation) = 32)
) STRICT;
CREATE INDEX account_mail_owner ON account_mail_owners(incarnation,outbox_id);

CREATE TABLE account_signup_owners (
    proof_id TEXT PRIMARY KEY REFERENCES signup_proofs(id) ON DELETE CASCADE,
    incarnation TEXT NOT NULL CHECK(length(incarnation) = 32)
) STRICT;

CREATE TRIGGER lifecycle_initialization_monotone BEFORE UPDATE OF stage ON lifecycle_binding
WHEN NOT ((OLD.stage='new' AND NEW.stage='prepared') OR (OLD.stage='prepared' AND NEW.stage='ready'))
BEGIN
    SELECT RAISE(ABORT, 'lifecycle initialization cannot be reset');
END;
