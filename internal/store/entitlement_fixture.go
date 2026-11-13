package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

const fixtureOrdinaryOperations = 1024
const fixtureAccountLimit = 256

type FixtureEntitlementOperation struct {
	ID, Generation, Incarnation, State string
	UserID                             int64
}

func fixtureSyntheticAccount(tx *gorm.DB, user int64, incarnation string) (bool, error) {
	var email string
	err := tx.Raw(`SELECT u.email FROM users u JOIN account_incarnations a ON a.user_id=u.user_id
WHERE u.user_id=? AND a.incarnation=? AND u.email_verified_at>0`, user, incarnation).Row().Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	display, _, err := EmailIdentity(email)
	return err == nil && strings.HasSuffix(strings.ToLower(display[strings.LastIndexByte(display, '@')+1:]), ".test"), nil
}

func retireFixtureEntitlements(tx *gorm.DB) error {
	for _, query := range []string{
		"DELETE FROM entitlement_grants WHERE source='fixture'",
		"DELETE FROM entitlement_fixture_operations",
		"UPDATE entitlement_fixture SET generation=lower(hex(randomblob(16))),lease_id=NULL,lease_expires_ns=NULL,ordinary_used=0 WHERE singleton=1",
	} {
		if err := tx.Exec(query).Error; err != nil {
			return err
		}
	}
	return nil
}

// FixtureEntitlementGeneration binds an authenticated fixture configuration to
// the database. Returning the same generation on restart preserves all receipts.
func (s *Store) FixtureEntitlementGeneration(ctx context.Context, lease FixtureEntitlementLease, clock func() time.Time) (string, error) {
	if lease.ID == "" {
		return "", ErrPersonalNotFound
	}
	var generation string
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := clock()
		if !now.Before(lease.Expires) {
			return ErrPersonalNotFound
		}
		var current sql.NullString
		var expires sql.NullInt64
		if err := tx.Raw("SELECT generation,lease_id,lease_expires_ns FROM entitlement_fixture WHERE singleton=1").Row().Scan(&generation, &current, &expires); err != nil {
			return err
		}
		if current.Valid && current.String == lease.ID {
			if !expires.Valid || expires.Int64 != lease.Expires.UnixNano() {
				return ErrPersonalNotFound
			}
			return nil
		}
		at := now.UnixNano()
		if err := tx.Exec("DELETE FROM entitlement_retired_leases WHERE expires_ns<=?", at).Error; err != nil {
			return err
		}
		var retired bool
		if err := tx.Raw("SELECT EXISTS(SELECT 1 FROM entitlement_retired_leases WHERE id=?)", lease.ID).Row().Scan(&retired); err != nil {
			return err
		}
		if retired {
			return ErrPersonalNotFound
		}
		if current.Valid {
			if expires.Int64 > at {
				if err := tx.Exec("INSERT INTO entitlement_retired_leases VALUES(?,?)", current.String, expires.Int64).Error; err != nil {
					return err
				}
			}
			if err := retireFixtureEntitlements(tx); err != nil {
				return err
			}
		}
		return tx.Raw("UPDATE entitlement_fixture SET lease_id=?,lease_expires_ns=? WHERE singleton=1 RETURNING generation", lease.ID, lease.Expires.UnixNano()).Row().Scan(&generation)
	})
	return generation, err
}

// SetFixtureEntitlement applies one authenticated fixture operation, including
// its receipt and capacity accounting, in a single SQLite write transaction.
func (s *Store) SetFixtureEntitlement(ctx context.Context, lease FixtureEntitlementLease, op FixtureEntitlementOperation, clock func() time.Time) error {
	if lease.ID == "" || !validPersonalID(op.ID) || !validPersonalID(op.Generation) ||
		(op.State != "premium" && op.State != "expired" && op.State != "free") {
		return ErrPersonalNotFound
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := clock()
		if !now.Before(lease.Expires) {
			return ErrPersonalNotFound
		}
		var ordinary int
		err := tx.Raw(`SELECT ordinary_used FROM entitlement_fixture WHERE singleton=1
AND generation=? AND lease_id=? AND lease_expires_ns=?`, op.Generation, lease.ID, lease.Expires.UnixNano()).Row().Scan(&ordinary)
		if err != nil {
			return err
		}
		allowed, err := fixtureSyntheticAccount(tx, op.UserID, op.Incarnation)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrPersonalNotFound
		}
		var existing string
		err = tx.Raw("SELECT state FROM entitlement_fixture_operations WHERE generation=? AND incarnation=? AND operation_id=?", op.Generation, op.Incarnation, op.ID).Row().Scan(&existing)
		if err == nil {
			if existing == op.State {
				return nil
			}
			return errEntitlementConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		at := now.UnixNano()
		reserved := ordinary >= fixtureOrdinaryOperations
		if reserved && op.State != "free" {
			return ErrPersonalPermission
		}
		if op.State == "premium" || reserved {
			var active bool
			if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM entitlement_grants WHERE source='fixture'
AND generation=? AND lease_id=? AND incarnation=? AND revoked_ns IS NULL AND ends_ns>?)`,
				op.Generation, lease.ID, op.Incarnation, at).Row().Scan(&active); err != nil {
				return err
			}
			if reserved && !active {
				return ErrPersonalPermission
			}
			if !active && op.State == "premium" {
				var owners int
				if err := tx.Raw(`SELECT COUNT(DISTINCT incarnation) FROM entitlement_grants WHERE source='fixture'
AND generation=? AND lease_id=? AND revoked_ns IS NULL AND ends_ns>?`, op.Generation, lease.ID, at).Row().Scan(&owners); err != nil {
					return err
				}
				if owners >= fixtureAccountLimit {
					return ErrPersonalPermission
				}
			}
		}
		if op.State != "premium" {
			if err := tx.Exec(`UPDATE entitlement_grants SET revoked_ns=? WHERE source='fixture'
AND generation=? AND lease_id=? AND incarnation=? AND revoked_ns IS NULL AND ends_ns>?`,
				at, op.Generation, lease.ID, op.Incarnation, at).Error; err != nil {
				return err
			}
		}
		if op.State != "free" {
			hash := sha256.Sum256([]byte(op.Generation + ":" + op.Incarnation + ":" + op.ID))
			g := entitlementGrant{id: hex.EncodeToString(hash[:]), incarnation: op.Incarnation, source: "fixture",
				generation: op.Generation, lease: lease.ID, start: at, end: lease.Expires.UnixNano()}
			if op.State == "expired" {
				g.start, g.end = at-int64(time.Second), at
			}
			if err := insertEntitlementGrant(tx, g); err != nil {
				return err
			}
		}
		if !reserved {
			if err := tx.Exec("UPDATE entitlement_fixture SET ordinary_used=ordinary_used+1 WHERE singleton=1").Error; err != nil {
				return err
			}
		}
		return tx.Exec("INSERT INTO entitlement_fixture_operations VALUES(?,?,?,?,?)", op.Generation, op.Incarnation, op.ID, op.State, reserved).Error
	})
}
