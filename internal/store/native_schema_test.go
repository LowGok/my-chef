package store

import (
	"path/filepath"
	"testing"

	"github.com/LowGok/my-chef/migrations"
)

func TestNativeMigrationUpgradesSchema16WithoutRewritingAccounts(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "upgrade.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// Construct the actual preceding SQL schema independently of Migrate's new step.
	for _, sql := range []string{migrations.Initial, migrations.Shopping, migrations.Favorites, migrations.Email,
		migrations.EmailIdentity, migrations.EmailLimits, migrations.SignUp, migrations.EmailVerification,
		migrations.EmailChange, migrations.PasswordRecovery, migrations.Feedback, migrations.AccountLifecycle,
		migrations.PersonalRecipes, migrations.PersonalPhotos, migrations.PersonalPlanning, migrations.RecipeImports} {
		if _, err := s.SQL.ExecContext(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SQL.ExecContext(t.Context(), `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,dirty BOOLEAN NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SQL.ExecContext(t.Context(), "INSERT INTO schema_migrations VALUES(16,?,0)", recipeImportSchemaChecksum()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SQL.ExecContext(t.Context(), `INSERT INTO users(firstname,lastname,email,email_key,password,username) VALUES('Upgrade','Test','upgrade@example.test','upgrade@example.test','unchanged-password-hash','upgrade');
INSERT INTO sessions VALUES('existing-browser-digest',1,9999999999);`); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := s.SQL.QueryRow("SELECT sql FROM sqlite_master WHERE name='users'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal("repeat explicit migration failed")
	}
	if err := s.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	var after, password string
	var sessions int
	if err := s.SQL.QueryRow("SELECT sql FROM sqlite_master WHERE name='users'").Scan(&after); err != nil || after != before {
		t.Fatal("account table was rewritten")
	}
	if err := s.SQL.QueryRow("SELECT password FROM users WHERE user_id=1").Scan(&password); err != nil || password != "unchanged-password-hash" {
		t.Fatal("existing account credential changed")
	}
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM sessions WHERE hash='existing-browser-digest'").Scan(&sessions); err != nil || sessions != 1 {
		t.Fatal("existing browser session changed")
	}
	if err := s.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
}
