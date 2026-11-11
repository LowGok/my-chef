package store

import (
	"context"
	"testing"
)

func versionTwelvePersonalFixture(t *testing.T) *Store {
	t.Helper()
	s := versionFourEmailStore(t, "personal-upgrade@example.test")
	for _, step := range []func(context.Context) error{s.migrateEmailIdentity, s.migrateEmailLimits, s.migrateSignUp,
		s.migrateEmailVerification, s.migrateEmailChange, s.migratePasswordRecovery, s.migrateFeedback, s.migrateAccountLifecycle} {
		if err := step(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestPersonalMigrationPreservesExistingIdentityAndSchema(t *testing.T) {
	s := versionTwelvePersonalFixture(t)
	var beforeUser, beforeIdentity, beforeBinding string
	for query, target := range map[string]*string{
		"SELECT sql FROM sqlite_schema WHERE name='users'":                    &beforeUser,
		"SELECT incarnation FROM account_incarnations WHERE user_id=1":        &beforeIdentity,
		"SELECT binding||':'||stage FROM lifecycle_binding WHERE singleton=1": &beforeBinding,
	} {
		if err := s.SQL.QueryRowContext(t.Context(), query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal("migration replay failed", err)
	}
	for query, want := range map[string]string{
		"SELECT sql FROM sqlite_schema WHERE name='users'":                    beforeUser,
		"SELECT incarnation FROM account_incarnations WHERE user_id=1":        beforeIdentity,
		"SELECT binding||':'||stage FROM lifecycle_binding WHERE singleton=1": beforeBinding,
	} {
		var got string
		if err := s.SQL.QueryRowContext(t.Context(), query).Scan(&got); err != nil || got != want {
			t.Fatal("upgrade changed existing identity/schema", err)
		}
	}
	var version int
	if err := s.SQL.QueryRow("SELECT version FROM schema_migrations").Scan(&version); err != nil || version != len(schemaVersions) {
		t.Fatal("personal migration absent", version, err)
	}
	if err := s.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPersonalMigrationFailureKeepsOldDatabaseUntouched(t *testing.T) {
	s := versionTwelvePersonalFixture(t)
	if _, err := s.SQL.Exec("CREATE TABLE personal_recipes(id TEXT); INSERT INTO personal_recipes VALUES('keep-existing-content')"); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(t.Context()); err == nil {
		t.Fatal("conflicting schema was replaced")
	}
	var version, partial int
	var existing string
	if err := s.SQL.QueryRow("SELECT version FROM schema_migrations").Scan(&version); err != nil || version != 12 {
		t.Fatal("failed upgrade changed metadata", version, err)
	}
	if err := s.SQL.QueryRow("SELECT id FROM personal_recipes").Scan(&existing); err != nil || existing != "keep-existing-content" {
		t.Fatal("foreign content was overwritten", err)
	}
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('personal_recipe_versions','personal_recipe_operations','account_incarnation_pair')").Scan(&partial); err != nil || partial != 0 {
		t.Fatal("partial schema survived rollback", partial, err)
	}
}
