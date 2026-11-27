package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestNativeIdentityPolicyCutoffAndCompareAndSwap(t *testing.T) {
	s := nativeIdentityPriorStore(t)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	p, err := s.SetNativeIdentityPolicy(t.Context(), 0, true, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetNativeIdentityPolicy(t.Context(), 0, false, 0, now); err == nil {
		t.Fatal("stale operator changed current policy")
	}
	s.NativeIdentityWork.RLock()
	done := make(chan error, 1)
	go func() {
		_, err := s.SetNativeIdentityPolicy(context.Background(), p.Revision, false, 0, now)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("cutoff finished while admitted work still running: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	s.NativeIdentityWork.RUnlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	current, err := s.NativeIdentityPolicy(t.Context())
	if err != nil || current.Active || current.SyncUntil != 0 {
		t.Fatal("withdrawal not persisted", err)
	}
	if _, err := s.SetNativeIdentityPolicy(t.Context(), current.Revision, false, now.Add(-time.Second).Unix(), now); err == nil {
		t.Fatal("expired sync window accepted")
	}
}

func TestNativeIdentityDeadlineIsRecheckedInsideShoppingTransaction(t *testing.T) {
	s := shoppingStore(t)
	ctx := t.Context()
	if _, err := s.SetNativeIdentityPolicy(ctx, 0, true, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{
		&Session{Hash: "app-anchor", UserID: Ptr(int64(1)), ExpiresUnix: 9999999999},
		&NativeFamily{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", AnchorHash: "app-anchor", Platform: "ios", ExpiresUnix: 9999999999, IdentityLane: "app", IdentityRevision: 1},
		&Session{Hash: "app-access", UserID: Ptr(int64(1)), ExpiresUnix: 9999999999},
		&NativeAccess{Hash: "app-access", FamilyID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	} {
		if err := s.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetNativeIdentityPolicy(ctx, 1, false, time.Now().Add(time.Minute).Unix(), time.Now()); err != nil {
		t.Fatal(err)
	}
	actor := PersonalActor{UserID: 1, SessionHash: "app-access", Native: true}
	bound := WithNativeIdentityAuthority(ctx, 1)
	if err := s.DB.WithContext(bound).Transaction(func(tx *gorm.DB) error { return nativeActorAuthority(tx, 1, actor) }); err != nil {
		t.Fatal("approved sync family denied", err)
	}
	if _, err := s.SQL.Exec("UPDATE native_identity_policy SET sync_until=unixepoch()-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.WithContext(bound).Transaction(func(tx *gorm.DB) error { return nativeActorAuthority(tx, 1, actor) }); !errors.Is(err, ErrAccountAccess) {
		t.Fatal("late shopping transaction escaped deadline", err)
	}
}

func TestNativeIdentityPolicyMigrationFailureIsAtomic(t *testing.T) {
	s := nativeIdentityPriorStore(t)
	if err := s.migrateNativeIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	minimisationSQL(t, s, "CREATE TRIGGER reject_identity_policy BEFORE UPDATE ON schema_migrations WHEN NEW.version=30 BEGIN SELECT RAISE(ABORT,'identity policy fixture'); END")
	if err := s.Migrate(t.Context()); err == nil {
		t.Fatal("injected migration failure ignored")
	}
	var version, columns, policies int
	if err := s.SQL.QueryRow("SELECT version FROM schema_migrations").Scan(&version); err != nil || version != 29 {
		t.Fatal("failed migration changed prior schema", err)
	}
	if err := s.SQL.QueryRow("SELECT count(*) FROM pragma_table_info('native_families') WHERE name='identity_revision'").Scan(&columns); err != nil || columns != 0 {
		t.Fatal("partial family revision retained", err)
	}
	if err := s.SQL.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='native_identity_policy'").Scan(&policies); err != nil || policies != 0 {
		t.Fatal("partial policy retained", err)
	}
	minimisationSQL(t, s, "DROP TRIGGER reject_identity_policy")
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal("explicit policy migration retry failed", err)
	}
	policy, err := s.NativeIdentityPolicy(t.Context())
	if err != nil || policy.Active || policy.Revision != 0 {
		t.Fatal("migration enabled candidate", err)
	}
}
