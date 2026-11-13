package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// Historical upgrade tests permit only the requested additive user column.
func withoutNewsletterColumn(schema string) string {
	return strings.Replace(schema, ", isNewsletterOnly BOOLEAN NOT NULL DEFAULT 0 CHECK(isNewsletterOnly IN (0,1))", "", 1)
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
