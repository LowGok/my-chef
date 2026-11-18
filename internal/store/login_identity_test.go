package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/LowGok/my-chef/migrations"
)

func legacySignInIdentityStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sign-in-legacy.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	legacy := nativeSchemaSQL() + migrations.EntitlementGrants + migrations.Analytics +
		migrations.AnalyticsGeneration + migrations.AnalyticsInstallation + migrations.AnalyticsSignupSource +
		migrations.AnalyticsGaps + migrations.Newsletter + migrations.WeeklyReminders +
		migrations.ReminderDispatchExpiry + migrations.NewsletterIdentityMinimisation
	minimisationSQL(t, s, legacy)
	minimisationSQL(t, s, "CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,dirty BOOLEAN NOT NULL)")
	minimisationSQL(t, s, "INSERT INTO schema_migrations VALUES(27,?,0)", newsletterIdentitySchemaChecksum())
	minimisationSQL(t, s, `INSERT INTO users(user_id,firstname,lastname,email,email_key,password,username,isNewsletterOnly)
		VALUES(101,'First','Synthetic','first@example.test','first@example.test','first-hash','Étage',0),
		      (102,'Second','Synthetic','second@example.test','second@example.test','second-hash','étage',0),
		      (103,'','','newsletter@example.test','newsletter@example.test','','ÉTAGE',1);
		INSERT INTO sessions VALUES('retained-session',101,9999999999);`)
	return s
}

func TestSignInIdentifierMigrationIndexesLegacyUnicodeCollisions(t *testing.T) {
	s := legacySignInIdentityStore(t)
	var err error
	if err = s.Migrate(t.Context()); err != nil {
		t.Fatalf("upgrade disposable old schema: %v", err)
	}
	if err = s.CheckSchema(t.Context()); err != nil {
		t.Fatalf("accept upgraded schema: %v", err)
	}
	var exact int
	if err = s.SQL.QueryRowContext(t.Context(), `SELECT count(*) FROM users WHERE
		(user_id=101 AND username='Étage' AND email_key='first@example.test' AND password='first-hash') OR
		(user_id=102 AND username='étage' AND email_key='second@example.test' AND password='second-hash') OR
		(user_id=103 AND username='ÉTAGE' AND isNewsletterOnly=1)`).Scan(&exact); err != nil || exact != 3 {
		t.Fatalf("account identity changed: %d %v", exact, err)
	}
	if err = s.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM sessions WHERE user_id=101 AND hash='retained-session'").Scan(&exact); err != nil || exact != 1 {
		t.Fatalf("existing session changed: %d %v", exact, err)
	}
	const query = "SELECT count(*) FROM users WHERE isNewsletterOnly=0 AND login_username_key = ?"
	key := []byte("étage")
	if err = s.SQL.QueryRowContext(t.Context(), query, key).Scan(&exact); err != nil {
		t.Fatalf("no usable folded-key lookup index yet: %v", err)
	}
	if exact != 2 {
		t.Fatalf("expected both legacy app accounts to remain ambiguous, got %d", exact)
	}
	if err = s.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM users WHERE isNewsletterOnly=1 AND login_username_key IS NOT NULL").Scan(&exact); err != nil || exact != 0 {
		t.Fatalf("newsletter-only account received a key: %d %v", exact, err)
	}
	var node, parent, reserved int
	var detail string
	if err = s.SQL.QueryRowContext(t.Context(), "EXPLAIN QUERY PLAN "+query, key).Scan(&node, &parent, &reserved, &detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "SEARCH") || !strings.Contains(detail, "users_app_login_username_key") {
		t.Fatalf("lookup is not constrained by the folded-key index: %s", detail)
	}
}

func TestSignInIdentifierMigrationFailureRollsBackAndCanRetry(t *testing.T) {
	s := legacySignInIdentityStore(t)
	minimisationSQL(t, s, `CREATE TRIGGER fixture_abort_after_first_key BEFORE UPDATE ON users
		WHEN old.user_id=102 BEGIN SELECT RAISE(ABORT,'fixture migration fault'); END`)
	if err := s.Migrate(t.Context()); err == nil || !strings.Contains(err.Error(), "fixture migration fault") {
		t.Fatalf("expected the injected backfill failure, got %v", err)
	}
	var version, columns, indexes int
	var dirty bool
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty); err != nil || version != 27 || dirty {
		t.Fatalf("failed migration changed schema metadata: %d %v %v", version, dirty, err)
	}
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_table_info('users') WHERE name='login_username_key'").Scan(&columns); err != nil || columns != 0 {
		t.Fatal("failed migration left a derived column behind", err)
	}
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name='users_app_login_username_key'").Scan(&indexes); err != nil || indexes != 0 {
		t.Fatal("failed migration left an index behind", err)
	}
	minimisationSQL(t, s, "DROP TRIGGER fixture_abort_after_first_key")
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal("atomic migration could not be retried", err)
	}
	if err := s.CheckSchema(t.Context()); err != nil {
		t.Fatal("retried migration did not produce a compatible schema", err)
	}
}

func TestSignInIdentifierSharedCreateDerivesKeysWithoutDiscardingImports(t *testing.T) {
	s := minimisationSchemaStore(t)
	for _, user := range []User{
		{Email: "unicode-one@example.test", Username: "Étage", Password: "fixture-password"},
		{Email: "unicode-two@example.test", Username: "étage", Password: "fixture-password"},
		{Email: "padded@example.test", Username: " Padded ", Password: "fixture-password"},
		{Email: "newsletter@example.test", Username: "ÉTAGE", IsNewsletterOnly: true},
	} {
		if err := s.DB.Create(&user).Error; err != nil {
			t.Fatalf("shared create lost legacy record: %v", err)
		}
	}
	var count int
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM users WHERE isNewsletterOnly=0 AND login_username_key=?", []byte("étage")).Scan(&count); err != nil || count != 2 {
		t.Fatalf("shared create did not preserve the legacy ambiguity: %d %v", count, err)
	}
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM users WHERE login_username_key IS NULL").Scan(&count); err != nil || count != 2 {
		t.Fatalf("unsupported or newsletter row received a folded key: %d %v", count, err)
	}
}

// Existing migration tests compare all prior columns and constraints exactly;
// only the additive column from schema 28 is excluded from that old baseline.
func withoutLoginUsernameColumn(schema string) string {
	return strings.Replace(schema, ", login_username_key BLOB", "", 1)
}
