package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/LowGok/my-chef/migrations"
)

func newsletterIdentitySchemaChecksum() string {
	sum := sha256.Sum256([]byte(reminderDispatchExpirySchemaChecksum() + migrations.NewsletterIdentityMinimisation))
	return hex.EncodeToString(sum[:])
}

func (s *Store) migrateNewsletterIdentity(ctx context.Context) error {
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
	if version != 26 || checksum != reminderDispatchExpirySchemaChecksum() || dirty {
		return errors.New("unexpected schema before newsletter identity migration")
	}
	if _, err := tx.ExecContext(ctx, migrations.NewsletterIdentityMinimisation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_migrations SET version=27,checksum=?,dirty=0 WHERE version=26", newsletterIdentitySchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

// NewsletterIdentityMatch stores no delivery address. The namespace key binding
// and the current journal must be verified before resolving or creating owners.
type NewsletterIdentityMatch struct {
	UserID      int64 `gorm:"primaryKey"`
	Incarnation string
	KeyVersion  int
	AddressTag  string
}

func (NewsletterIdentityMatch) TableName() string { return "newsletter_identity_matches" }

type NewsletterIdentityNamespace struct {
	Singleton              int `gorm:"primaryKey"`
	KeyVersion             int
	KeyID                  string
	PhysicalCleanupPending bool
	LegacyVacuumPending    bool
}

func (NewsletterIdentityNamespace) TableName() string { return "newsletter_identity_namespace" }
