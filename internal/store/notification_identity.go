package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/LowGok/my-chef/migrations"
)

func notificationIdentitySchemaChecksum() string {
	sum := sha256.Sum256([]byte(nativeIdentityPolicySchemaChecksum() + migrations.NotificationIdentity))
	return hex.EncodeToString(sum[:])
}

func (s *Store) migrateNotificationIdentity(ctx context.Context) error {
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
	if version != 30 || checksum != nativeIdentityPolicySchemaChecksum() || dirty {
		return errors.New("unexpected schema before notification identity migration")
	}
	if _, err := tx.ExecContext(ctx, migrations.NotificationIdentity); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_migrations SET version=31,checksum=?,dirty=0 WHERE version=30", notificationIdentitySchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

type reminderIdentityRoute struct {
	environment string
	supports    func(string) bool
}

// SetIdentityProviderRoute installs immutable server configuration before
// serving. No request parameter can select a provider environment.
func (s *ReminderSettings) SetIdentityProviderRoute(lane, environment string, supports func(string) bool) error {
	if (lane != "preview" && lane != "app") || (environment != "production" && environment != "development") || supports == nil {
		return ErrReminderInput
	}
	if s.identityRoutes == nil {
		s.identityRoutes = map[string]reminderIdentityRoute{}
	}
	s.identityRoutes[lane] = reminderIdentityRoute{environment, supports}
	return nil
}

func (s *ReminderSettings) reminderRoute(lane string) (reminderIdentityRoute, bool) {
	if route, ok := s.identityRoutes[lane]; ok {
		return route, true
	}
	if lane == "preview" {
		return reminderIdentityRoute{"production", s.providerEnabled}, true
	}
	return reminderIdentityRoute{}, false
}

func (s *ReminderSettings) identityProviderEnabled(lane, environment, provider string) bool {
	route, ok := s.reminderRoute(lane)
	return ok && route.environment == environment && route.supports(provider)
}
