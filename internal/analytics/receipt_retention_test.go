package analytics_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/analytics"
	"gorm.io/gorm"
)

func TestClosedReceiptsFollowRetainedHistory(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, err := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web")
	if err != nil {
		t.Fatal(err)
	}
	capture := f.s.Request(ctx, f.user, p.Revision, "web")
	if err = f.app.DB.WithContext(capture).Transaction(func(tx *gorm.DB) error {
		return analytics.CaptureEvent(capture, tx, f.user, "meal_plan_saved", "receipt-history")
	}); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Deliver(ctx, nil); err != nil {
		t.Fatal(err)
	}
	_ = f.s.Close()
	*f.now = f.now.Add(time.Hour)
	opts := f.opts
	opts.Create = false
	opts.Config.Assessment.Version = "fixture-v2"
	next, err := analytics.Open(ctx, f.app.DB, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Close() }()
	f.s = next
	f.app.Analytics = next
	p, err = f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1", ExpectedRevision: p.Revision}, "web")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = f.app.SQL.QueryRow("SELECT count(*) FROM analytics_receipts").Scan(&n); err != nil || n != 2 {
		t.Fatalf("required receipts %d %v", n, err)
	}
	// A closed-month contribution still needs the earlier enrollment authority
	// after its raw event has expired.
	*f.now = f.now.Add(20 * 24 * time.Hour)
	if _, err = f.s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(80 * 24 * time.Hour)
	if err = f.s.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.app.SQL.QueryRow("SELECT count(*) FROM analytics_receipts").Scan(&n); err != nil || n != 2 {
		t.Fatalf("monthly provenance receipts %d %v", n, err)
	}
	if _, err = f.s.Refresh(ctx); err != nil {
		t.Fatal("historical provenance refresh", err)
	}
	journal, err := sql.Open("sqlite3", filepath.Join(f.opts.Root, "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var encoded string
	err = journal.QueryRow("SELECT provenance FROM contributions WHERE month='2026-11' LIMIT 1").Scan(&encoded)
	_ = journal.Close()
	if err != nil || !strings.Contains(encoded, "fixture-assessment") || !strings.Contains(encoded, "fixture-v2") {
		t.Fatalf("historical authority overwritten: %s %v", encoded, err)
	}
	if _, err = f.s.Update(ctx, f.user, analytics.PreferenceUpdate{NoticeVersion: "core-v1", ExpectedRevision: p.Revision}, "web"); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.app.SQL.QueryRow("SELECT count(*) FROM analytics_receipts").Scan(&n); err != nil || n != 0 {
		t.Fatalf("obsolete receipts retained %d %v", n, err)
	}
	p, err = f.s.Preference(ctx, f.user)
	if err != nil || p.Source != "objection" || p.Revision != 3 {
		t.Fatalf("minimal objection lost %+v %v", p, err)
	}
}
