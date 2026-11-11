package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"gorm.io/gorm"
)

func TestRecoveryRestoreUpgradesWithoutChangingIdentityOrSessions(t *testing.T) {
	ctx := context.Background()
	source := versionFourEmailStore(t, "Verified@Example.Test", "restricted@example.test")
	for _, step := range []func(context.Context) error{
		source.migrateEmailIdentity, source.migrateEmailLimits, source.migrateSignUp,
		source.migrateEmailVerification, source.migrateEmailChange,
	} {
		if err := step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := source.SQL.QueryRowContext(ctx, "UPDATE users SET email_verified_at=1000 WHERE user_id=1 RETURNING user_id").Scan(new(int64)); err != nil {
		t.Fatal(err)
	}
	id := int64(1)
	if err := source.DB.Create(&Session{Hash: "original-session", UserID: &id, ExpiresUnix: 2000}).Error; err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "recovery-restored.db")
	if err := source.publishSnapshot(ctx, target, true); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(target, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	if err := restored.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		db      *Store
		version int
	}{{source, 9}, {restored, len(schemaVersions)}} {
		var version int
		if err := pair.db.SQL.QueryRowContext(ctx, "SELECT version FROM schema_migrations").Scan(&version); err != nil || version != pair.version {
			t.Fatalf("schema: got version %d, want %d (%v)", version, pair.version, err)
		}
	}
	var saved User
	if err := restored.DB.First(&saved, id).Error; err != nil || saved.Email != "Verified@Example.Test" ||
		saved.Password != "legacy-hash" || saved.EmailVerifiedAt == nil || *saved.EmailVerifiedAt != 1000 {
		t.Fatal("upgrade changed the original account")
	}
	var session Session
	if err := restored.DB.Where("hash = ?", "original-session").Take(&session).Error; err != nil ||
		session.UserID == nil || *session.UserID != id || session.ExpiresUnix != 2000 {
		t.Fatal("upgrade changed the existing session")
	}
	var fence, latest int64
	if err := restored.SQL.QueryRowContext(ctx,
		"SELECT eligibility_order, latest_issued_order FROM recovery_account_orders WHERE user_id=1").Scan(&fence, &latest); err != nil ||
		fence != 0 || latest != 0 {
		t.Fatal("existing verified account did not start at the initial fence")
	}
	var restricted int
	if err := restored.SQL.QueryRowContext(ctx,
		"SELECT count(*) FROM recovery_account_orders WHERE user_id=2").Scan(&restricted); err != nil || restricted != 0 {
		t.Fatal("upgrade established an order for an unverified address")
	}
}

func TestRecoveryOrderPersistsAndRollsBackWithAccountWrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recovery-orders.db")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	user := User{FirstName: "Verified", LastName: "Fixture", Email: "order@example.test", Username: "order", Password: "fixture-hash"}
	if err := s.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := NextRecoveryOrder(ctx, s.DB); !errors.Is(err, ErrRecoveryUnavailable) {
		t.Fatal("an order must be allocated inside a write transaction")
	}
	abort := errors.New("injected abort")
	write := func(wantOrder int64, rollback bool) {
		t.Helper()
		err := s.DB.Transaction(func(tx *gorm.DB) error {
			order, err := AdvanceRecoveryFence(ctx, tx, user.ID)
			if err != nil {
				return err
			}
			if order != wantOrder {
				t.Errorf("allocated order %d, want %d", order, wantOrder)
			}
			if rollback {
				return abort
			}
			return nil
		})
		if rollback && !errors.Is(err, abort) || !rollback && err != nil {
			t.Fatalf("unexpected transaction result: %v", err)
		}
	}
	write(1, false)
	write(2, true)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	var fence int64
	if err := s.SQL.QueryRowContext(ctx, "SELECT eligibility_order FROM recovery_account_orders WHERE user_id=?", user.ID).Scan(&fence); err != nil ||
		fence != 1 {
		t.Fatalf("committed fence did not survive restart: %d (%v)", fence, err)
	}
	write(2, false)
	journal, err := s.InitializeLifecycleJournal(ctx, s.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	backup := filepath.Join(t.TempDir(), "ordered-backup.db")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	copy, err := Open(backup, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = copy.Close() })
	var order int64
	if err := copy.SQL.QueryRowContext(ctx, "SELECT sequence FROM recovery_clock WHERE singleton=1").Scan(&order); err != nil || order != 2 {
		t.Fatalf("backup lost recovery order: %d (%v)", order, err)
	}
}
