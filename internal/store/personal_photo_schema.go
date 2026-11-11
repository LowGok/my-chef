package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/LowGok/my-chef/migrations"
	"gorm.io/gorm"
)

func personalPhotoSchemaChecksum() string {
	sum := sha256.Sum256([]byte(migrations.Initial + migrations.Shopping + migrations.Favorites + migrations.Email +
		migrations.EmailIdentity + migrations.EmailLimits + migrations.SignUp + migrations.EmailVerification +
		migrations.EmailChange + migrations.PasswordRecovery + migrations.Feedback + migrations.AccountLifecycle + migrations.PersonalRecipes + migrations.PersonalPhotos))
	return hex.EncodeToString(sum[:])
}

func (s *Store) migratePersonalPhotos(ctx context.Context) error {
	tx, err := s.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var checksum string
	var dirty bool
	if err := tx.QueryRowContext(ctx, "SELECT version,checksum,dirty FROM schema_migrations").Scan(&version, &checksum, &dirty); err != nil {
		return err
	}
	if version != 13 || checksum != personalRecipeSchemaChecksum() || dirty {
		return errors.New("unexpected schema before personal photos migration")
	}
	if _, err := tx.ExecContext(ctx, migrations.PersonalPhotos); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_migrations SET version=14,checksum=?,dirty=0 WHERE version=13", personalPhotoSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

// Journal upgrades happen only during explicit migration. Older binaries reject v2.
func migratePersonalPhotoJournal(ctx context.Context, external *Store) error {
	return external.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var version int
		var binding string
		if err := tx.Raw("SELECT version,binding FROM lifecycle_journal_meta WHERE singleton=1").Row().Scan(&version, &binding); err != nil {
			return ErrLifecycleJournal
		}
		if version == 2 {
			return nil
		}
		if version != 1 || !opaqueLifecycleID(binding) {
			return ErrLifecycleJournal
		}
		return tx.Exec(`
DROP TRIGGER lifecycle_journal_binding_immutable;
DROP TRIGGER lifecycle_journal_binding_required;
CREATE TABLE lifecycle_journal_meta_v2(singleton INTEGER PRIMARY KEY CHECK(singleton=1), version INTEGER NOT NULL CHECK(version=2), binding TEXT NOT NULL CHECK(length(binding)=32)) STRICT;
INSERT INTO lifecycle_journal_meta_v2 SELECT singleton,2,binding FROM lifecycle_journal_meta;
DROP TABLE lifecycle_journal_meta;
ALTER TABLE lifecycle_journal_meta_v2 RENAME TO lifecycle_journal_meta;
CREATE TRIGGER lifecycle_journal_binding_immutable BEFORE UPDATE ON lifecycle_journal_meta BEGIN SELECT RAISE(ABORT,'immutable lifecycle binding'); END;
CREATE TRIGGER lifecycle_journal_binding_required BEFORE DELETE ON lifecycle_journal_meta BEGIN SELECT RAISE(ABORT,'required lifecycle binding'); END;
ALTER TABLE lifecycle_work ADD COLUMN delivery_type TEXT NOT NULL DEFAULT 'upload' CHECK(delivery_type IN ('upload','authenticated'));
ALTER TABLE lifecycle_work ADD COLUMN purpose TEXT NOT NULL DEFAULT 'account' CHECK(purpose IN ('account','personal'));
`).Error
	})
}
