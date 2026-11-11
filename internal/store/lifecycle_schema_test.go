package store

import (
	"context"
	"encoding/hex"
	"testing"
)

func lifecycleLegacyStore(t *testing.T) *Store {
	t.Helper()
	s := versionFourEmailStore(t, "owner@example.test", "other@example.test")
	for _, step := range []func(context.Context) error{s.migrateEmailIdentity, s.migrateEmailLimits, s.migrateSignUp, s.migrateEmailVerification, s.migrateEmailChange, s.migratePasswordRecovery, s.migrateFeedback} {
		if err := step(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestLifecycleMigrationPreservesRecordsAndIncarnations(t *testing.T) {
	s := lifecycleLegacyStore(t)
	ctx := t.Context()
	var definition string
	if err := s.SQL.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE name='users'").Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var first, other, binding string
	if err := s.SQL.QueryRowContext(ctx, "SELECT incarnation FROM account_incarnations WHERE user_id=1").Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := s.SQL.QueryRowContext(ctx, "SELECT incarnation FROM account_incarnations WHERE user_id=2").Scan(&other); err != nil {
		t.Fatal(err)
	}
	if err := s.SQL.QueryRowContext(ctx, "SELECT binding FROM lifecycle_binding WHERE singleton=1").Scan(&binding); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first, other, binding} {
		if b, err := hex.DecodeString(id); err != nil || len(b) != 16 {
			t.Fatal("invalid opaque incarnation")
		}
	}
	if first == other || first == binding {
		t.Fatal("identities collided")
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var again, after, email, password string
	if err := s.SQL.QueryRowContext(ctx, "SELECT incarnation FROM account_incarnations WHERE user_id=1").Scan(&again); err != nil || again != first {
		t.Fatal("migration changed incarnation", err)
	}
	if err := s.SQL.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE name='users'").Scan(&after); err != nil || after != definition {
		t.Fatal("existing users schema changed", err)
	}
	if err := s.SQL.QueryRowContext(ctx, "SELECT email,password FROM users WHERE user_id=1").Scan(&email, &password); err != nil || email != "owner@example.test" || password != "legacy-hash" {
		t.Fatal("existing data changed", err)
	}
	for _, query := range []string{"UPDATE account_incarnations SET incarnation=lower(hex(randomblob(16))) WHERE user_id=1", "UPDATE lifecycle_binding SET binding=lower(hex(randomblob(16))) WHERE singleton=1"} {
		if _, err := s.SQL.ExecContext(ctx, query); err == nil {
			t.Fatal("immutable identity update succeeded")
		}
	}
	if _, err := s.SQL.ExecContext(ctx, "DELETE FROM users WHERE user_id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SQL.ExecContext(ctx, "INSERT INTO users(user_id,firstname,lastname,email,email_key,password,username) VALUES(1,'New','User','owner@example.test','owner@example.test','new-hash','fresh')"); err != nil {
		t.Fatal(err)
	}
	if err := s.SQL.QueryRowContext(ctx, "SELECT incarnation FROM account_incarnations WHERE user_id=1").Scan(&again); err != nil || again == first || again == other {
		t.Fatal("reused numeric ID inherited old identity", err)
	}
	if err := s.Validate(ctx); err != nil {
		t.Fatal(err)
	}
}
