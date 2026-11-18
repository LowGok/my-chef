package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/LowGok/my-chef/migrations"
)

type NativeIdentityPolicy struct {
	Revision     int64
	Active       bool
	SyncUntil    int64
	SyncRevision int64
}

type nativeIdentityAuthorityKey struct{}

// WithNativeIdentityAuthority carries a trusted ingress revision to the final
// database write check. It is never populated from client request metadata.
func WithNativeIdentityAuthority(ctx context.Context, revision int64) context.Context {
	return context.WithValue(ctx, nativeIdentityAuthorityKey{}, revision)
}

func nativeIdentityPolicySchemaChecksum() string {
	sum := sha256.Sum256([]byte(nativeIdentitySchemaChecksum() + migrations.NativeIdentityPolicy))
	return hex.EncodeToString(sum[:])
}

func (s *Store) migrateNativeIdentityPolicy(ctx context.Context) error {
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
	if version != 29 || checksum != nativeIdentitySchemaChecksum() || dirty {
		return errors.New("unexpected schema before native identity policy migration")
	}
	if _, err := tx.ExecContext(ctx, migrations.NativeIdentityPolicy); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_migrations SET version=30,checksum=?,dirty=0 WHERE version=29", nativeIdentityPolicySchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

// NativeIdentityPolicy is read while NativeIdentityWork is held by the caller.
// The application holds an exclusive database process lock; HTTP admission and
// provider submission share this fence with the operator's cutoff.
func (s *Store) NativeIdentityPolicy(ctx context.Context) (NativeIdentityPolicy, error) {
	var p NativeIdentityPolicy
	err := s.SQL.QueryRowContext(ctx, "SELECT revision,active,sync_until,sync_revision FROM native_identity_policy WHERE id=1").Scan(&p.Revision, &p.Active, &p.SyncUntil, &p.SyncRevision)
	return p, err
}

// SetNativeIdentityPolicy is an operator operation, never an HTTP admin route.
// Comparing revisions prevents stale retries from reopening a withdrawn lane.
func (s *Store) SetNativeIdentityPolicy(ctx context.Context, expected int64, active bool, syncUntil int64, now time.Time) (NativeIdentityPolicy, error) {
	if expected < 0 || syncUntil < 0 || active && syncUntil != 0 || syncUntil != 0 && syncUntil <= now.Unix() {
		return NativeIdentityPolicy{}, errors.New("invalid native identity policy")
	}
	s.NativeIdentityWork.Lock()
	defer s.NativeIdentityWork.Unlock()
	current, err := s.NativeIdentityPolicy(ctx)
	if err != nil {
		return NativeIdentityPolicy{}, err
	}
	if current.Revision != expected || syncUntil != 0 && !current.Active {
		return NativeIdentityPolicy{}, errors.New("native identity policy changed; reconcile current state")
	}
	next := NativeIdentityPolicy{Revision: current.Revision + 1, Active: active, SyncUntil: syncUntil}
	if syncUntil != 0 {
		next.SyncRevision = current.Revision
	}
	result, err := s.SQL.ExecContext(ctx, "UPDATE native_identity_policy SET revision=?,active=?,sync_until=?,sync_revision=? WHERE id=1 AND revision=?", next.Revision, next.Active, next.SyncUntil, next.SyncRevision, current.Revision)
	if err != nil {
		return NativeIdentityPolicy{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return NativeIdentityPolicy{}, err
	}
	if rows != 1 {
		return NativeIdentityPolicy{}, errors.New("native identity policy changed; reconcile current state")
	}
	return next, nil
}
