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

// The native transport requires a live short-lived actor at the write itself,
// including mutations whose generated-recipe path predates personal recipes.
func nativeActorAuthority(tx *gorm.DB, owner int64, actors ...PersonalActor) error {
	for _, actor := range actors {
		if !actor.Native {
			continue
		}
		if actor.UserID != owner || owner <= 0 || actor.SessionHash == "" {
			return ErrAccountAccess
		}
		var count int64
		if err := tx.Model(&Session{}).Where("hash=? AND user_id=? AND expires_unix>unixepoch()", actor.SessionHash, owner).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrAccountAccess
		}
	}
	return nil
}

func nativePlanningAuthority(tx *gorm.DB, owner int64, options []PersonalPlanning) error {
	for _, option := range options {
		if err := nativeActorAuthority(tx, owner, option.Actor); err != nil {
			return err
		}
	}
	return nil
}

func nativeSchemaChecksum() string {
	sum := sha256.Sum256([]byte(nativeSchemaSQL()))
	return hex.EncodeToString(sum[:])
}

func nativeSchemaSQL() string {
	return migrations.Initial + migrations.Shopping + migrations.Favorites + migrations.Email + migrations.EmailIdentity + migrations.EmailLimits + migrations.SignUp + migrations.EmailVerification + migrations.EmailChange + migrations.PasswordRecovery + migrations.Feedback + migrations.AccountLifecycle + migrations.PersonalRecipes + migrations.PersonalPhotos + migrations.PersonalPlanning + migrations.RecipeImports + migrations.NativeSessions
}

func (s *Store) migrateNativeSessions(ctx context.Context) error {
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
	if version != 16 || checksum != recipeImportSchemaChecksum() || dirty {
		return errors.New("unexpected schema before native session migration")
	}
	if _, err := tx.ExecContext(ctx, migrations.NativeSessions); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_migrations SET version=17,checksum=?,dirty=0 WHERE version=16", nativeSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}
