package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/LowGok/my-chef/migrations"
)

func newsletterSchemaChecksum() string {
	sum := sha256.Sum256([]byte(entitlementSchemaChecksum() + migrations.Newsletter))
	return hex.EncodeToString(sum[:])
}

func (s *Store) migrateNewsletter(ctx context.Context) error {
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
	if version != 18 || checksum != entitlementSchemaChecksum() || dirty {
		return errors.New("unexpected schema before newsletter migration")
	}
	if _, err := tx.ExecContext(ctx, migrations.Newsletter); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_migrations SET version=19,checksum=?,dirty=0 WHERE version=18", newsletterSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}
