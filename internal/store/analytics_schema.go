package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"

	"github.com/LowGok/my-chef/migrations"
	"gorm.io/gorm"
)

func analyticsSchemaChecksum() string {
	h := sha256.Sum256([]byte(nativeSchemaSQL() + migrations.EntitlementGrants + migrations.Analytics))
	return fmt.Sprintf("%x", h)
}
func (s *Store) migrateAnalytics(ctx context.Context) error {
	tx, err := s.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var checksum string
	var dirty bool
	if err = tx.QueryRowContext(ctx, "SELECT version,checksum,dirty FROM schema_migrations").Scan(&version, &checksum, &dirty); err != nil {
		return err
	}
	if version != 18 || checksum != entitlementSchemaChecksum() || dirty {
		return errors.New("unexpected schema before analytics migration")
	}
	if _, err = tx.ExecContext(ctx, migrations.Analytics); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_migrations SET version=19,checksum=?,dirty=0 WHERE version=18", analyticsSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

func analyticsGenerationChecksum() string {
	h := sha256.Sum256([]byte(nativeSchemaSQL() + migrations.EntitlementGrants + migrations.Analytics + migrations.AnalyticsGeneration))
	return fmt.Sprintf("%x", h)
}
func (s *Store) migrateAnalyticsGeneration(ctx context.Context) error {
	tx, err := s.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var checksum string
	var dirty bool
	if err = tx.QueryRowContext(ctx, "SELECT version,checksum,dirty FROM schema_migrations").Scan(&version, &checksum, &dirty); err != nil {
		return err
	}
	if version != 19 || checksum != analyticsSchemaChecksum() || dirty {
		return errors.New("unexpected schema before analytics job migration")
	}
	if _, err = tx.ExecContext(ctx, migrations.AnalyticsGeneration); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_migrations SET version=20,checksum=?,dirty=0 WHERE version=19", analyticsGenerationChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

func analyticsInstallationChecksum() string {
	h := sha256.Sum256([]byte(nativeSchemaSQL() + migrations.EntitlementGrants + migrations.Analytics + migrations.AnalyticsGeneration + migrations.AnalyticsInstallation))
	return fmt.Sprintf("%x", h)
}
func (s *Store) migrateAnalyticsInstallation(ctx context.Context) error {
	tx, err := s.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var checksum string
	var dirty bool
	if err = tx.QueryRowContext(ctx, "SELECT version,checksum,dirty FROM schema_migrations").Scan(&version, &checksum, &dirty); err != nil {
		return err
	}
	if version != 20 || checksum != analyticsGenerationChecksum() || dirty {
		return errors.New("unexpected schema before analytics installation migration")
	}
	if _, err = tx.ExecContext(ctx, migrations.AnalyticsInstallation); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_migrations SET version=21,checksum=?,dirty=0 WHERE version=20", analyticsInstallationChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) analyticsRequired(ctx context.Context) bool {
	var n int
	err := s.SQL.QueryRowContext(ctx, "SELECT required FROM analytics_installation WHERE singleton=1").Scan(&n)
	return err != nil || n != 0
}
func (s *Store) stripAnalytics(ctx context.Context) error {
	if _, err := s.SQL.ExecContext(ctx, "PRAGMA secure_delete=ON"); err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, table := range []string{"analytics_outbox", "analytics_generation", "analytics_receipts", "analytics_preferences", "analytics_signup", "analytics_gaps"} {
			if err := tx.Exec("DELETE FROM " + table).Error; err != nil {
				return err
			}
		}
		return tx.Exec("UPDATE analytics_installation SET restore_paused=required WHERE singleton=1").Error
	})
}

func analyticsSignupSourceChecksum() string {
	h := sha256.Sum256([]byte(nativeSchemaSQL() + migrations.EntitlementGrants + migrations.Analytics + migrations.AnalyticsGeneration + migrations.AnalyticsInstallation + migrations.AnalyticsSignupSource))
	return fmt.Sprintf("%x", h)
}
func (s *Store) migrateAnalyticsSignupSource(ctx context.Context) error {
	tx, err := s.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var checksum string
	var dirty bool
	if err = tx.QueryRowContext(ctx, "SELECT version,checksum,dirty FROM schema_migrations").Scan(&version, &checksum, &dirty); err != nil {
		return err
	}
	if version != 21 || checksum != analyticsInstallationChecksum() || dirty {
		return errors.New("unexpected schema before analytics signup source migration")
	}
	if _, err = tx.ExecContext(ctx, migrations.AnalyticsSignupSource); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_migrations SET version=22,checksum=?,dirty=0 WHERE version=21", analyticsSignupSourceChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

func analyticsGapsChecksum() string {
	h := sha256.Sum256([]byte(nativeSchemaSQL() + migrations.EntitlementGrants + migrations.Analytics + migrations.AnalyticsGeneration + migrations.AnalyticsInstallation + migrations.AnalyticsSignupSource + migrations.AnalyticsGaps))
	return fmt.Sprintf("%x", h)
}
func (s *Store) migrateAnalyticsGaps(ctx context.Context) error {
	tx, err := s.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var checksum string
	var dirty bool
	if err = tx.QueryRowContext(ctx, "SELECT version,checksum,dirty FROM schema_migrations").Scan(&version, &checksum, &dirty); err != nil {
		return err
	}
	if version != 22 || checksum != analyticsSignupSourceChecksum() || dirty {
		return errors.New("unexpected schema before analytics gap migration")
	}
	if _, err = tx.ExecContext(ctx, migrations.AnalyticsGaps); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_migrations SET version=23,checksum=?,dirty=0 WHERE version=22", analyticsGapsChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}
