package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/LowGok/my-chef/migrations"
)

func personalRecipeSchemaChecksum() string {
	sum := sha256.Sum256([]byte(migrations.Initial + migrations.Shopping + migrations.Favorites + migrations.Email +
		migrations.EmailIdentity + migrations.EmailLimits + migrations.SignUp + migrations.EmailVerification +
		migrations.EmailChange + migrations.PasswordRecovery + migrations.Feedback + migrations.AccountLifecycle + migrations.PersonalRecipes))
	return hex.EncodeToString(sum[:])
}

func (s *Store) migratePersonalRecipes(ctx context.Context) error {
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
	if version != 12 || checksum != accountLifecycleSchemaChecksum() || dirty {
		return errors.New("unexpected schema before personal recipes migration")
	}
	if _, err := tx.ExecContext(ctx, migrations.PersonalRecipes); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_migrations SET version=13,checksum=?,dirty=0 WHERE version=12", personalRecipeSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}
