package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAnalyticsExplicitMigration(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "app.sqlite"), true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	u := User{FirstName: "Migration", LastName: "Fixture", Email: "migration@example.invalid", Username: "migration", Password: "hash"}
	if e = s.DB.Create(&u).Error; e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"analytics_preferences", "analytics_receipts", "analytics_outbox"} {
		var n int
		if e = s.SQL.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil || n != 0 {
			t.Fatalf("historical backfill %s: %d %v", table, n, e)
		}
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Validate(ctx); e != nil {
		t.Fatal(e)
	}
}
