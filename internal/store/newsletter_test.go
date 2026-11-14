package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LowGok/my-chef/migrations"
)

// Historical upgrade tests permit only the requested additive user column.
func withoutNewsletterColumn(schema string) string {
	return strings.Replace(schema, ", isNewsletterOnly BOOLEAN NOT NULL DEFAULT 0 CHECK(isNewsletterOnly IN (0,1))", "", 1)
}

func TestNewsletterUpgradeFromPublishedAnalyticsSchema(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "published.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	legacy := nativeSchemaSQL() + migrations.EntitlementGrants + migrations.Analytics + migrations.AnalyticsGeneration + migrations.AnalyticsInstallation + migrations.AnalyticsSignupSource + migrations.AnalyticsGaps
	if _, err := s.SQL.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SQL.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,dirty BOOLEAN NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SQL.Exec(`INSERT INTO schema_migrations VALUES(23,?,0)`, analyticsGapsChecksum()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SQL.Exec(`INSERT INTO users(user_id,email,email_key,username,password,firstname,lastname) VALUES(47,'existing@example.test','existing@example.test','existing','retained-hash','Existing','Account');
INSERT INTO analytics_preferences(incarnation,enabled,revision,changed_unix) SELECT incarnation,0,3,1234 FROM account_incarnations WHERE user_id=47`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	var password string
	var contact bool
	if err := s.SQL.QueryRow(`SELECT password,isNewsletterOnly FROM users WHERE user_id=47`).Scan(&password, &contact); err != nil || password != "retained-hash" || contact {
		t.Fatal("existing account changed", err)
	}
	var enabled, revision, changed int
	if err := s.SQL.QueryRow(`SELECT enabled,revision,changed_unix FROM analytics_preferences`).Scan(&enabled, &revision, &changed); err != nil || enabled != 0 || revision != 3 || changed != 1234 {
		t.Fatal("analytics choice changed", err)
	}
}

func TestNewsletterUnpublishedSchemaIsNotAcceptedAsPublishedAnalytics(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "unpublished.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.SQL.Exec(nativeSchemaSQL() + migrations.EntitlementGrants + migrations.Newsletter); err != nil {
		t.Fatal(err)
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(entitlementSchemaChecksum()+migrations.Newsletter)))
	if _, err := s.SQL.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,dirty BOOLEAN NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SQL.Exec(`INSERT INTO schema_migrations VALUES(19,?,0)`, checksum); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(t.Context()); err == nil {
		t.Fatal("ambiguous unpublished newsletter schema was accepted")
	}
	var actual string
	if err := s.SQL.QueryRow(`SELECT checksum FROM schema_migrations WHERE version=19 AND dirty=0`).Scan(&actual); err != nil || actual != checksum {
		t.Fatal("rejected database was changed", err)
	}
}

func TestNewsletterMigrationPreservesAccounts(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "newsletter.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	u := User{Email: "account@example.test", Username: "account", Password: "existing-hash", FirstName: "Existing", LastName: "Account"}
	if err := s.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	var newsletterOnly bool
	if err := s.SQL.QueryRow(`SELECT isNewsletterOnly FROM users WHERE user_id=?`, u.ID).Scan(&newsletterOnly); err != nil {
		t.Fatal(err)
	}
	if newsletterOnly {
		t.Fatal("existing account classified as newsletter-only")
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.SQL.QueryRow(`SELECT count(*) FROM newsletter_subscriptions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("subscription migration: %d %v", count, err)
	}
	if _, err := s.SQL.Exec(`UPDATE users SET isNewsletterOnly=2 WHERE user_id=?`, u.ID); err == nil {
		t.Fatal("invalid boolean accepted")
	}
	var password string
	if err := s.SQL.QueryRow(`SELECT password FROM users WHERE user_id=?`, u.ID).Scan(&password); err != nil || password != u.Password {
		t.Fatal("account credential changed")
	}
}
