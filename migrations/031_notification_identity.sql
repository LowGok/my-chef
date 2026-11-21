ALTER TABLE reminder_installations ADD COLUMN identity_lane TEXT NOT NULL DEFAULT 'preview' CHECK(identity_lane IN ('preview','app'));
ALTER TABLE reminder_installations ADD COLUMN environment TEXT NOT NULL DEFAULT 'production' CHECK(environment IN ('production','development'));
ALTER TABLE reminder_installations ADD COLUMN identity_revision INTEGER NOT NULL DEFAULT 0 CHECK(identity_revision>=0);
ALTER TABLE reminder_dispatches ADD COLUMN identity_lane TEXT NOT NULL DEFAULT 'preview' CHECK(identity_lane IN ('preview','app'));
ALTER TABLE reminder_dispatches ADD COLUMN environment TEXT NOT NULL DEFAULT 'production' CHECK(environment IN ('production','development'));
ALTER TABLE reminder_dispatches ADD COLUMN identity_revision INTEGER NOT NULL DEFAULT 0 CHECK(identity_revision>=0);

CREATE TRIGGER reminder_installation_identity_insert BEFORE INSERT ON reminder_installations
WHEN NOT EXISTS(SELECT 1 FROM native_families f WHERE f.id=NEW.family_id AND f.identity_lane=NEW.identity_lane AND f.identity_revision=NEW.identity_revision)
BEGIN SELECT RAISE(ABORT,'reminder family identity mismatch'); END;
CREATE TRIGGER reminder_installation_identity_update BEFORE UPDATE OF identity_lane,environment,identity_revision ON reminder_installations
WHEN NEW.identity_lane<>OLD.identity_lane OR NEW.environment<>OLD.environment OR NEW.identity_revision<>OLD.identity_revision
BEGIN SELECT RAISE(ABORT,'reminder installation identity is immutable'); END;
CREATE TRIGGER reminder_dispatch_identity_update BEFORE UPDATE OF identity_lane,environment,identity_revision ON reminder_dispatches
WHEN NEW.identity_lane<>OLD.identity_lane OR NEW.environment<>OLD.environment OR NEW.identity_revision<>OLD.identity_revision
BEGIN SELECT RAISE(ABORT,'reminder dispatch identity is immutable'); END;
