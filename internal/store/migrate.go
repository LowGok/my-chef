package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/LowGok/my-chef/migrations"
)

func schemaChecksum() string {
	sum := sha256.Sum256([]byte(migrations.Initial))
	return hex.EncodeToString(sum[:])
}

func shoppingSchemaChecksum() string {
	sum := sha256.Sum256([]byte(migrations.Initial + migrations.Shopping))
	return hex.EncodeToString(sum[:])
}

func favoritesSchemaChecksum() string {
	sum := sha256.Sum256([]byte(migrations.Initial + migrations.Shopping + migrations.Favorites))
	return hex.EncodeToString(sum[:])
}

func emailSchemaChecksum() string {
	sum := sha256.Sum256([]byte(migrations.Initial + migrations.Shopping + migrations.Favorites + migrations.Email))
	return hex.EncodeToString(sum[:])
}

func emailIdentitySchemaChecksum() string {
	sum := sha256.Sum256([]byte(migrations.Initial + migrations.Shopping + migrations.Favorites + migrations.Email + migrations.EmailIdentity))
	return hex.EncodeToString(sum[:])
}

func emailLimitsSchemaChecksum() string {
	sum := sha256.Sum256([]byte(migrations.Initial + migrations.Shopping + migrations.Favorites +
		migrations.Email + migrations.EmailIdentity + migrations.EmailLimits))
	return hex.EncodeToString(sum[:])
}

func signupSchemaChecksum() string {
	sum := sha256.Sum256([]byte(migrations.Initial + migrations.Shopping + migrations.Favorites +
		migrations.Email + migrations.EmailIdentity + migrations.EmailLimits + migrations.SignUp))
	return hex.EncodeToString(sum[:])
}

func emailVerificationSchemaChecksum() string {
	sum := sha256.Sum256([]byte(migrations.Initial + migrations.Shopping + migrations.Favorites +
		migrations.Email + migrations.EmailIdentity + migrations.EmailLimits + migrations.SignUp + migrations.EmailVerification))
	return hex.EncodeToString(sum[:])
}

func emailChangeSchemaChecksum() string {
	sum := sha256.Sum256([]byte(migrations.Initial + migrations.Shopping + migrations.Favorites +
		migrations.Email + migrations.EmailIdentity + migrations.EmailLimits + migrations.SignUp + migrations.EmailVerification +
		migrations.EmailChange))
	return hex.EncodeToString(sum[:])
}

func passwordRecoverySchemaChecksum() string {
	sum := sha256.Sum256([]byte(migrations.Initial + migrations.Shopping + migrations.Favorites +
		migrations.Email + migrations.EmailIdentity + migrations.EmailLimits + migrations.SignUp + migrations.EmailVerification +
		migrations.EmailChange + migrations.PasswordRecovery))
	return hex.EncodeToString(sum[:])
}

func feedbackSchemaChecksum() string {
	sum := sha256.Sum256([]byte(migrations.Initial + migrations.Shopping + migrations.Favorites +
		migrations.Email + migrations.EmailIdentity + migrations.EmailLimits + migrations.SignUp + migrations.EmailVerification +
		migrations.EmailChange + migrations.PasswordRecovery + migrations.Feedback))
	return hex.EncodeToString(sum[:])
}

var schemaVersions = []func() string{schemaChecksum, shoppingSchemaChecksum, favoritesSchemaChecksum,
	emailSchemaChecksum, emailIdentitySchemaChecksum, emailLimitsSchemaChecksum, signupSchemaChecksum, emailVerificationSchemaChecksum, emailChangeSchemaChecksum, passwordRecoverySchemaChecksum, feedbackSchemaChecksum, accountLifecycleSchemaChecksum, personalRecipeSchemaChecksum, personalPhotoSchemaChecksum, personalPlanningSchemaChecksum, recipeImportSchemaChecksum, nativeSchemaChecksum, entitlementSchemaChecksum}

func (s *Store) CheckSchema(ctx context.Context) error {
	return s.checkSchema(ctx, false)
}

func (s *Store) checkSchema(ctx context.Context, allowPriorBackup bool) error {
	var version int
	var checksum string
	var dirty bool
	if err := s.SQL.QueryRowContext(ctx, "SELECT version, checksum, dirty FROM schema_migrations").Scan(&version, &checksum, &dirty); err != nil {
		return errors.New("run the explicit database migration first")
	}
	var count int
	if err := s.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		return err
	}
	known := version > 0 && version <= len(schemaVersions) && checksum == schemaVersions[version-1]()
	allowed := version == len(schemaVersions) || allowPriorBackup && version >= 3
	if count != 1 || dirty || !known || !allowed {
		return errors.New("unknown, changed or dirty database migration")
	}
	return nil
}

func (s *Store) Migrate(ctx context.Context) error {
	var meta int
	if err := s.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&meta); err != nil {
		return err
	}
	if meta == 0 {
		var tables int
		if err := s.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			return err
		}
		if tables != 0 {
			return errors.New("refusing an unversioned existing database")
		}
	}
	if _, err := s.SQL.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,dirty BOOLEAN NOT NULL)"); err != nil {
		return err
	}
	var count int
	if err := s.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		var version int
		var checksum string
		var dirty bool
		if count != 1 {
			return errors.New("invalid migration metadata")
		}
		if err := s.SQL.QueryRowContext(ctx, "SELECT version,checksum,dirty FROM schema_migrations").Scan(&version, &checksum, &dirty); err != nil {
			return err
		}
		if dirty || version < 1 || version > len(schemaVersions) || checksum != schemaVersions[version-1]() {
			return errors.New("unknown, changed or dirty database migration")
		}
		return s.migrateAfter(ctx, version)
	}
	// The marker survives a failed migration and requires recovery rather than a blind retry.
	if _, err := s.SQL.ExecContext(ctx, "INSERT INTO schema_migrations(version,checksum,dirty) VALUES(1,?,1)", schemaChecksum()); err != nil {
		return err
	}
	tx, err := s.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, migrations.Initial); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_migrations SET dirty=0 WHERE version=1"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.migrateAfter(ctx, 1)
}

func (s *Store) migrateAfter(ctx context.Context, version int) error {
	steps := []func(context.Context) error{s.migrateShopping, s.migrateFavorites, s.migrateEmail,
		s.migrateEmailIdentity, s.migrateEmailLimits, s.migrateSignUp, s.migrateEmailVerification, s.migrateEmailChange, s.migratePasswordRecovery, s.migrateFeedback, s.migrateAccountLifecycle, s.migratePersonalRecipes, s.migratePersonalPhotos, s.migratePersonalPlanning, s.migrateRecipeImports, s.migrateNativeSessions, s.migrateEntitlements}
	for _, step := range steps[version-1:] {
		if err := step(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateEmail(ctx context.Context) error {
	if _, err := s.SQL.ExecContext(ctx, "UPDATE schema_migrations SET dirty=1 WHERE version=3"); err != nil {
		return err
	}
	tx, err := s.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, migrations.Email); err != nil {
		return fmt.Errorf("email migration failed: %w", err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_migrations SET version=4,checksum=?,dirty=0 WHERE version=3", emailSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateShopping(ctx context.Context) error {
	if _, err := s.SQL.ExecContext(ctx, "UPDATE schema_migrations SET dirty=1 WHERE version=1"); err != nil {
		return err
	}
	tx, err := s.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, migrations.Shopping); err != nil {
		return fmt.Errorf("shopping migration failed: %w", err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_migrations SET version=2,checksum=?,dirty=0 WHERE version=1", shoppingSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateFavorites(ctx context.Context) error {
	if _, err := s.SQL.ExecContext(ctx, "UPDATE schema_migrations SET dirty=1 WHERE version=2"); err != nil {
		return err
	}
	tx, err := s.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, migrations.Favorites); err != nil {
		return fmt.Errorf("favorites migration failed: %w", err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_migrations SET version=3,checksum=?,dirty=0 WHERE version=2", favoritesSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateFeedback(ctx context.Context) error {
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
	if version != 10 || checksum != passwordRecoverySchemaChecksum() || dirty {
		return errors.New("unexpected schema before feedback migration")
	}
	if _, err := tx.ExecContext(ctx, migrations.Feedback); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_migrations SET version=11,checksum=?,dirty=0 WHERE version=10", feedbackSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}
