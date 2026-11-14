package main

import (
	"path/filepath"
	"testing"

	"github.com/LowGok/my-chef/internal/store"
)

func TestAnalyticsStartsDisabledWithoutOperatorConfiguration(t *testing.T) {
	app, e := store.Open(filepath.Join(t.TempDir(), "app.sqlite"), true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = app.Close() }()
	if e = app.Migrate(t.Context()); e != nil {
		t.Fatal(e)
	}
	close, e := openAnalytics(t.Context(), app, "")
	if e != nil {
		t.Fatal(e)
	}
	defer close()
	if app.Analytics != nil {
		t.Fatal("default started live analytics")
	}
	var n int
	if e = app.SQL.QueryRow("SELECT count(*) FROM analytics_outbox").Scan(&n); e != nil || n != 0 {
		t.Fatal("default collected events", e)
	}
}
