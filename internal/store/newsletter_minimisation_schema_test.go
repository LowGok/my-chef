package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LowGok/my-chef/migrations"
)

func minimisationSchemaStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "identity.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s
}

func minimisationSQL(t *testing.T, s *Store, statement string, arguments ...any) {
	t.Helper()
	if _, err := s.SQL.ExecContext(t.Context(), statement, arguments...); err != nil {
		t.Fatal(err)
	}
}

func TestNewsletterMinimisationSchemaSQLiteCapability(t *testing.T) {
	s := minimisationSchemaStore(t)
	var engine string
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT sqlite_version()").Scan(&engine); err != nil {
		t.Fatal(err)
	}
	t.Log("actual pinned SQLite engine:", engine)
	// Exercise the application's driver, never Python's unrelated SQLite build.
	minimisationSQL(t, s, "CREATE TABLE alter_probe(email TEXT NOT NULL UNIQUE)")
	minimisationSQL(t, s, "INSERT INTO alter_probe VALUES('retained@example.test')")
	minimisationSQL(t, s, "ALTER TABLE alter_probe ALTER email DROP NOT NULL")
	minimisationSQL(t, s, "INSERT INTO alter_probe VALUES(NULL),(NULL)")
	var retained int
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM alter_probe WHERE email='retained@example.test'").Scan(&retained); err != nil || retained != 1 {
		t.Fatal("native ALTER changed an existing identity", err)
	}
}

func TestNewsletterMinimisationSchemaPublished26Upgrade(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "published26.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	legacy := nativeSchemaSQL() + migrations.EntitlementGrants + migrations.Analytics + migrations.AnalyticsGeneration + migrations.AnalyticsInstallation + migrations.AnalyticsSignupSource + migrations.AnalyticsGaps + migrations.Newsletter + migrations.WeeklyReminders + migrations.ReminderDispatchExpiry
	if fmt.Sprintf("%x", sha256.Sum256([]byte(legacy))) != "c655b52c3c074e5acbcd9c5b1827ca69be071f08ba63f71dcaa128f520fd91f6" {
		t.Fatal("published SQL inputs changed")
	}
	const released26 = "b292a56709de30aafd0037f8480bcdee6c8de6668e59d02608bf36c8eb1b00e6"
	if reminderDispatchExpirySchemaChecksum() != released26 {
		t.Fatal("published schema-26 checksum changed")
	}
	minimisationSQL(t, s, legacy)
	minimisationSQL(t, s, "CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,dirty BOOLEAN NOT NULL)")
	minimisationSQL(t, s, "INSERT INTO schema_migrations VALUES(26,?,0)", released26)
	minimisationSQL(t, s, `INSERT INTO users(user_id,firstname,lastname,email,email_key,password,username,haspref,preferences_json)
VALUES(47,'Existing','Account','account@example.test','account@example.test','retained-password-hash','existing',1,'{"household":2}');
INSERT INTO users(user_id,firstname,lastname,email,email_key,password,username,isNewsletterOnly)
VALUES(48,'','','contact@example.test','contact@example.test','','newsletter-opaque',1);
INSERT INTO newsletter_subscriptions(user_id,email_key,status,locale,consent_version,source,requested_unix)
VALUES(48,'contact@example.test','pending','en','notice-test','/blog/',1000);
INSERT INTO sessions VALUES('retained-session',47,9999999999);
INSERT INTO menus(id,user_id,created_unix) VALUES(11,47,1000);
INSERT INTO meals(meal_id,creator_id,menu_id,creationdate,recipetitle) VALUES(93,47,11,'2026-11-15','Retained meal');
INSERT INTO analytics_preferences(incarnation,enabled,revision,changed_unix)
SELECT incarnation,0,3,1234 FROM account_incarnations WHERE user_id=47`)
	var incarnation string
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT incarnation FROM account_incarnations WHERE user_id=47").Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	indexes := map[string]string{}
	rows, err := s.SQL.QueryContext(t.Context(), "SELECT name,sql FROM sqlite_schema WHERE type='index' AND sql IS NOT NULL")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatal(err)
		}
		indexes[name] = definition
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal("explicit upgrade", err)
	}
	var columns int
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_table_info('users') WHERE name='newsletter_minimised'").Scan(&columns); err != nil || columns != 1 {
		t.Fatal("upgrade did not add the explicit reduced identity state", err)
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal("repeat explicit migration", err)
	}
	if err := s.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(t.Context()); err != nil {
		t.Fatal("mixed-store integrity or foreign keys changed", err)
	}
	for name, definition := range indexes {
		var after string
		if err := s.SQL.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_schema WHERE name=?", name).Scan(&after); err != nil || after != definition {
			t.Fatalf("published index %s changed: %v", name, err)
		}
	}
	var password, profile, afterIncarnation string
	var session, meal, optedOut, revision int
	if err := s.SQL.QueryRowContext(t.Context(), `SELECT u.password,u.preferences_json,a.incarnation,
(SELECT count(*) FROM sessions WHERE hash='retained-session' AND user_id=47),
(SELECT count(*) FROM meals WHERE meal_id=93 AND creator_id=47 AND menu_id=11),
(SELECT enabled FROM analytics_preferences WHERE incarnation=a.incarnation),
(SELECT revision FROM analytics_preferences WHERE incarnation=a.incarnation)
FROM users u JOIN account_incarnations a ON a.user_id=u.user_id WHERE u.user_id=47`).Scan(&password, &profile, &afterIncarnation, &session, &meal, &optedOut, &revision); err != nil || password != "retained-password-hash" || profile != `{"household":2}` || afterIncarnation != incarnation || session != 1 || meal != 1 || optedOut != 0 || revision != 3 {
		t.Fatal("upgrade changed the full account, relationships or analytics choice", err)
	}
	var address, username string
	var only, minimised bool
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT email,username,isNewsletterOnly,newsletter_minimised FROM users WHERE user_id=48").Scan(&address, &username, &only, &minimised); err != nil || address != "contact@example.test" || username != "newsletter-opaque" || !only || minimised {
		t.Fatal("schema upgrade silently minimised an unbound contact", err)
	}
}

func TestNewsletterMinimisationSchemaConditionalIdentity(t *testing.T) {
	s := minimisationSchemaStore(t)
	minimisationSQL(t, s, `INSERT INTO users(user_id,firstname,lastname,email,email_key,password,username,isNewsletterOnly)
VALUES(1,'','','inactive@example.test','inactive@example.test','','opaque',1)`)
	minimisationSQL(t, s, `UPDATE users SET newsletter_minimised=1,email=NULL,email_key=NULL,username=NULL WHERE user_id=1`)
	for _, update := range []string{
		"isNewsletterOnly=0", "newsletter_minimised=0", "newsletter_minimised=2",
		"password='app-credential'", "email_verified_at=1", "firstname='App profile'",
		"preferences_json='{}'", "email='unexpected@example.test'", "email_key='unexpected@example.test'", "username='unexpected'",
	} {
		t.Run(update, func(t *testing.T) {
			if _, err := s.SQL.ExecContext(t.Context(), "UPDATE users SET "+update+" WHERE user_id=1"); err == nil {
				t.Fatal("invalid reduced identity accepted")
			}
		})
	}
	minimisationSQL(t, s, `INSERT INTO users(user_id,firstname,lastname,email,email_key,password,username)
VALUES(2,'Full','Account','full@example.test','full@example.test','hash','full')`)
	for _, update := range []string{"email=NULL", "email_key=NULL", "email_key=''", "username=NULL", "newsletter_minimised=1"} {
		t.Run("full/"+update, func(t *testing.T) {
			if _, err := s.SQL.ExecContext(t.Context(), "UPDATE users SET "+update+" WHERE user_id=2"); err == nil {
				t.Fatal("full-account identity constraint weakened")
			}
		})
	}
	if _, err := s.SQL.ExecContext(t.Context(), `INSERT INTO users(firstname,lastname,email,email_key,password,username)
VALUES('Duplicate','Account','FULL@example.test','full@example.test','hash','duplicate')`); err == nil {
		t.Fatal("canonical full-account uniqueness lost")
	}
	var email, key, username sql.NullString
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT email,email_key,username FROM users WHERE user_id=1").Scan(&email, &key, &username); err != nil || email.Valid || key.Valid || username.Valid {
		t.Fatal("absent identity data was replaced with fabricated values", err)
	}
	minimisationSQL(t, s, `INSERT INTO newsletter_subscriptions(user_id,email_key,status,locale,consent_version,source,requested_unix)
VALUES(1,NULL,'unsubscribed',NULL,'retained-notice',NULL,NULL)`)
	if _, err := s.SQL.ExecContext(t.Context(), `INSERT INTO newsletter_subscriptions(user_id,email_key,status,locale,consent_version,source,requested_unix)
VALUES(2,NULL,'pending',NULL,'notice',NULL,NULL)`); err == nil {
		t.Fatal("missing full-account subscription identity accepted")
	}
}

func TestNewsletterMinimisationSchemaProtectedOwnership(t *testing.T) {
	s := minimisationSchemaStore(t)
	incarnation := strings.Repeat("a", 32)
	tag := strings.Repeat("b", 64)
	// A current authoritative journal can reconstruct a missing reduced contact
	// atomically with its original incarnation, without disabling FK protection.
	tx, err := s.SQL.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), "INSERT INTO newsletter_identity_matches(user_id,incarnation,key_version,address_tag) VALUES(9,?,1,?)", incarnation, tag); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO users(user_id,firstname,lastname,email,email_key,password,username,isNewsletterOnly,newsletter_minimised)
VALUES(9,'','',NULL,NULL,'',NULL,1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("same-ID/incarnation reconstruction failed", err)
	}
	var restored string
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT incarnation FROM account_incarnations WHERE user_id=9").Scan(&restored); err != nil || restored != incarnation {
		t.Fatal("reconstructed incarnation changed", err)
	}
	minimisationSQL(t, s, `INSERT INTO users(user_id,firstname,lastname,email,email_key,password,username,isNewsletterOnly)
VALUES(10,'','','other@example.test','other@example.test','','other',1)`)
	var other string
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT incarnation FROM account_incarnations WHERE user_id=10").Scan(&other); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]any{
		{10, other, 1, tag},
		{10, strings.Repeat("c", 32), 1, strings.Repeat("c", 64)},
		{10, other, 2, strings.Repeat("c", 64)},
		{10, other, 1, "not-a-protected-tag"},
	} {
		if _, err := s.SQL.ExecContext(t.Context(), "INSERT INTO newsletter_identity_matches(user_id,incarnation,key_version,address_tag) VALUES(?,?,?,?)", arguments...); err == nil {
			t.Fatal("duplicate, mismatched or unsupported ownership accepted")
		}
	}
	minimisationSQL(t, s, "DELETE FROM users WHERE user_id=9")
	var matches int
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM newsletter_identity_matches WHERE user_id=9").Scan(&matches); err != nil || matches != 0 {
		t.Fatal("deleted SQL owner retained an active match", err)
	}
	if err := s.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
}
