package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/LowGok/my-chef/migrations"
)

func nativeIdentitySchemaChecksum() string {
	sum := sha256.Sum256([]byte(loginUsernameSchemaChecksum() + migrations.NativeIdentityLanes))
	return hex.EncodeToString(sum[:])
}

func (s *Store) migrateNativeIdentity(ctx context.Context) error {
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
	if version != 28 || checksum != loginUsernameSchemaChecksum() || dirty {
		return errors.New("unexpected schema before native identity migration")
	}
	if _, err := tx.ExecContext(ctx, migrations.NativeIdentityLanes); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_migrations SET version=29,checksum=?,dirty=0 WHERE version=28", nativeIdentitySchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}
