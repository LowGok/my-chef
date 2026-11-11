package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LowGok/my-chef/migrations"
)

func versionFourEmailStore(t *testing.T, addresses ...string) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "v4.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.SQL.ExecContext(ctx, migrations.Initial+migrations.Shopping+migrations.Favorites+migrations.Email+
		"CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,dirty BOOLEAN NOT NULL);"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SQL.ExecContext(ctx, "INSERT INTO schema_migrations VALUES(4,?,0)", emailSchemaChecksum()); err != nil {
		t.Fatal(err)
	}
	for i, email := range addresses {
		if _, err := s.SQL.ExecContext(ctx,
			"INSERT INTO users(user_id,firstname,lastname,email,password,username) VALUES(?, 'Old', 'User', ?, 'legacy-hash', ?)",
			i+1, email, email); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestEmailIdentityUpgradeAndRestore(t *testing.T) {
	ctx := context.Background()
	source := versionFourEmailStore(t, "Straße+Test@Example.Test")
	target := filepath.Join(t.TempDir(), "restored.db")
	if err := source.publishSnapshot(ctx, target, true); err != nil {
		t.Fatal(err)
	}
	s, err := Open(target, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := source.SQL.QueryRowContext(ctx, "SELECT version FROM schema_migrations").Scan(&version); err != nil || version != 4 {
		t.Fatalf("restore modified its source: version %d, err %v", version, err)
	}
	var display, key, password string
	var verified bool
	err = s.SQL.QueryRowContext(ctx, "SELECT email,email_key,password,email_verified_at IS NOT NULL FROM users WHERE user_id=1").
		Scan(&display, &key, &password, &verified)
	if err != nil || display != "Straße+Test@Example.Test" || key != "strasse+test@example.test" || password != "legacy-hash" || verified {
		t.Fatalf("unexpected upgraded identity: delivery %q, key %q, verified %t, err %v", display, key, verified, err)
	}
	u := User{FirstName: "New", LastName: "User", Email: "  Other@Example.Test ", Username: "new-user", Password: "fixture"}
	if err := s.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.SQL.QueryRowContext(ctx, "SELECT email,email_key FROM users WHERE user_id=?", u.ID).Scan(&display, &key); err != nil ||
		display != "Other@Example.Test" || key != "other@example.test" {
		t.Fatalf("unexpected new identity: delivery %q, key %q, err %v", display, key, err)
	}
	duplicate := User{FirstName: "New", LastName: "User", Email: "STRASSE+TEST@example.test", Username: "duplicate", Password: "fixture"}
	if err := s.DB.Create(&duplicate).Error; err == nil {
		t.Fatal("case-equivalent address bypassed the shared user write constraint")
	}
}

func TestEmailIdentityCollisionLeavesOriginalSchema(t *testing.T) {
	ctx := context.Background()
	s := versionFourEmailStore(t, "Same@Example.Test", "same@example.test")
	err := s.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "canonical email collision") || strings.Contains(strings.ToLower(err.Error()), "same@example.test") {
		t.Fatal("expected a generic canonical collision error")
	}
	var version, count int
	var dirty bool
	if err := s.SQL.QueryRowContext(ctx, "SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty); err != nil ||
		version != 4 || dirty {
		t.Fatalf("collision changed the migration marker: version %d, dirty %t, err %v", version, dirty, err)
	}
	if err := s.SQL.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('users') WHERE name='email_key'").Scan(&count); err != nil ||
		count != 0 {
		t.Fatalf("collision left a partial migration: count %d, err %v", count, err)
	}
	if err := s.SQL.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE password='legacy-hash' AND email IN ('Same@Example.Test','same@example.test')").Scan(&count); err != nil ||
		count != 2 {
		t.Fatalf("collision changed existing identities: count %d, err %v", count, err)
	}
}
