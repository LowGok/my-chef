package store

import (
	"strings"
	"testing"
)

func nativeIdentityPriorStore(t *testing.T) *Store {
	t.Helper()
	s := legacySignInIdentityStore(t)
	if err := s.migrateLoginUsernameKeys(t.Context()); err != nil {
		t.Fatal(err)
	}
	minimisationSQL(t, s, "INSERT INTO native_families(id,anchor_hash,platform,expires_unix) VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','retained-session','ios',9999999999)")
	return s
}

func TestNativeIdentityMigrationPreservesExistingFamilies(t *testing.T) {
	s := nativeIdentityPriorStore(t)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var lane, anchor, platform string
	var expires int64
	if err := s.SQL.QueryRow("SELECT identity_lane,anchor_hash,platform,expires_unix FROM native_families WHERE id='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'").Scan(&lane, &anchor, &platform, &expires); err != nil || lane != "preview" || anchor != "retained-session" || platform != "ios" || expires != 9999999999 {
		t.Fatal("identity migration rewrote an existing family", err)
	}
	if _, err := s.SQL.Exec("UPDATE native_families SET identity_lane='foreign' WHERE id='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'"); err == nil {
		t.Fatal("unknown identity persisted")
	}
	if err := s.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestNativeIdentityMigrationFailureCanRetryWithoutTouchingAccess(t *testing.T) {
	s := nativeIdentityPriorStore(t)
	minimisationSQL(t, s, "CREATE TRIGGER reject_identity_step BEFORE UPDATE ON schema_migrations WHEN NEW.version=29 BEGIN SELECT RAISE(ABORT,'identity migration fixture'); END")
	if err := s.Migrate(t.Context()); err == nil || !strings.Contains(err.Error(), "identity migration fixture") {
		t.Fatal("migration did not reach injected fault", err)
	}
	var version, columns, families int
	if err := s.SQL.QueryRow("SELECT version FROM schema_migrations").Scan(&version); err != nil || version != 28 {
		t.Fatal("failed migration advanced version", err)
	}
	if err := s.SQL.QueryRow("SELECT count(*) FROM pragma_table_info('native_families') WHERE name='identity_lane'").Scan(&columns); err != nil || columns != 0 {
		t.Fatal("failed migration retained partial schema", err)
	}
	if err := s.SQL.QueryRow("SELECT count(*) FROM native_families WHERE id='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'").Scan(&families); err != nil || families != 1 {
		t.Fatal("failed migration damaged original access", err)
	}
	minimisationSQL(t, s, "DROP TRIGGER reject_identity_step")
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal("explicit retry failed", err)
	}
}
