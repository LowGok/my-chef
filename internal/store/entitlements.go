package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/LowGok/my-chef/migrations"
	"gorm.io/gorm"
)

// FixtureEntitlementLease is server configuration, never account request data.
// ID is derived from the protected fixture configuration, not a bearer token.
type FixtureEntitlementLease struct {
	ID      string
	Expires time.Time
}

type entitlementGrant struct {
	id, incarnation, source, generation, lease string
	start, end                                 int64
}

var errEntitlementConflict = errors.New("entitlement grant identity has different facts")

func insertEntitlementGrant(tx *gorm.DB, g entitlementGrant) error {
	result := tx.Exec(`INSERT INTO entitlement_grants(id,incarnation,source,generation,lease_id,starts_ns,ends_ns)
VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, g.id, g.incarnation, g.source, g.generation, g.lease, g.start, g.end)
	if result.Error != nil || result.RowsAffected == 1 {
		return result.Error
	}
	var same bool
	err := tx.Raw(`SELECT incarnation=? AND source=? AND generation=? AND lease_id=? AND starts_ns=? AND ends_ns=?
FROM entitlement_grants WHERE id=?`, g.incarnation, g.source, g.generation, g.lease, g.start, g.end, g.id).Row().Scan(&same)
	if err != nil {
		return err
	}
	if !same {
		return errEntitlementConflict
	}
	return nil
}

// EntitlementReader uses the server clock even when a caller has been doing
// work since request admission. A zero lease is the normal production mode.
func EntitlementReader(lease FixtureEntitlementLease, now func() time.Time) PersonalEntitlementReader {
	if now == nil {
		now = time.Now
	}
	return func(ctx context.Context, tx *gorm.DB, user int64, incarnation string) (PersonalEntitlement, error) {
		at := now()
		if lease.ID == "" || !at.Before(lease.Expires) {
			return PersonalEntitlement{}, nil
		}
		eligible, err := fixtureSyntheticAccount(tx.WithContext(ctx), user, incarnation)
		if err != nil || !eligible {
			return PersonalEntitlement{}, err
		}
		return resolveEntitlement(ctx, tx, user, incarnation, lease, at)
	}
}

func resolveEntitlement(ctx context.Context, tx *gorm.DB, user int64, incarnation string, lease FixtureEntitlementLease, now time.Time) (PersonalEntitlement, error) {
	if lease.ID == "" || !now.Before(lease.Expires) {
		return PersonalEntitlement{}, nil
	}
	var until sql.NullInt64
	at := now.UnixNano()
	err := tx.WithContext(ctx).Raw(`SELECT MAX(g.ends_ns)
FROM entitlement_grants g
JOIN account_incarnations a ON a.incarnation=g.incarnation
JOIN entitlement_fixture f ON f.singleton=1 AND f.generation=g.generation AND f.lease_id=g.lease_id
WHERE a.user_id=? AND a.incarnation=? AND g.source='fixture' AND g.revoked_ns IS NULL
AND g.lease_id=? AND f.lease_expires_ns=? AND g.starts_ns<=? AND g.ends_ns>?`, user, incarnation, lease.ID, lease.Expires.UnixNano(), at, at).Row().Scan(&until)
	if err != nil || !until.Valid {
		return PersonalEntitlement{}, err
	}
	// Exact interval validity is decided above. Round up only for the existing
	// personal capability adapter, whose legacy expiry field is in seconds.
	return PersonalEntitlement{Premium: true, ExpiresUnix: (min(until.Int64, lease.Expires.UnixNano())-1)/int64(time.Second) + 1}, nil
}

func entitlementSchemaChecksum() string {
	sum := sha256.Sum256([]byte(nativeSchemaSQL() + migrations.EntitlementGrants))
	return hex.EncodeToString(sum[:])
}

func (s *Store) migrateEntitlements(ctx context.Context) error {
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
	if version != 17 || checksum != nativeSchemaChecksum() || dirty {
		return errors.New("unexpected schema before entitlement migration")
	}
	if _, err := tx.ExecContext(ctx, migrations.EntitlementGrants); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_migrations SET version=18,checksum=?,dirty=0 WHERE version=17", entitlementSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}
