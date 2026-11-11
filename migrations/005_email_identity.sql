ALTER TABLE users ADD COLUMN email_key TEXT;
ALTER TABLE users ADD COLUMN email_verified_at INTEGER;

CREATE UNIQUE INDEX users_email_key ON users(email_key);

CREATE TRIGGER users_email_key_insert BEFORE INSERT ON users
WHEN NEW.email_key IS NULL OR NEW.email_key = ''
BEGIN
    SELECT RAISE(ABORT, 'email identity required');
END;

CREATE TRIGGER users_email_key_update BEFORE UPDATE OF email_key ON users
WHEN NEW.email_key IS NULL OR NEW.email_key = ''
BEGIN
    SELECT RAISE(ABORT, 'email identity required');
END;
