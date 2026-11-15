package analytics_test

import (
	"context"
	"testing"

	"github.com/LowGok/my-chef/internal/analytics"
	"gorm.io/gorm"
)

func TestTransactionalCapture(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, e := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web")
	if e != nil {
		t.Fatal(e)
	}
	ctx = f.s.Request(ctx, f.user, p.Revision, "web")
	run := func(receipt string, rollback bool) {
		t.Helper()
		e := f.app.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if e := analytics.CaptureEvent(ctx, tx, f.user, "favorite_saved", receipt); e != nil {
				return e
			}
			if rollback {
				return analytics.ErrInput
			}
			return nil
		})
		if !rollback && e != nil {
			t.Fatal(e)
		}
	}
	run("save-1", false)
	run("save-1", false)
	run("save-2", true)
	var n int
	_ = f.app.SQL.QueryRow("SELECT count(*) FROM analytics_outbox").Scan(&n)
	if n != 1 {
		t.Fatalf("committed/replayed/rolled-back=%d", n)
	}
	stale := f.s.Request(context.Background(), f.user, 0, "web")
	if e = f.app.DB.WithContext(stale).Transaction(func(tx *gorm.DB) error { return analytics.CaptureEvent(stale, tx, f.user, "favorite_saved", "old") }); e != nil {
		t.Fatal(e)
	}
	_ = f.app.SQL.QueryRow("SELECT count(*) FROM analytics_outbox").Scan(&n)
	if n != 1 {
		t.Fatal("stale request captured")
	}
	_ = f.app.SQL.QueryRow("SELECT capture_gap FROM analytics_preferences").Scan(&n)
	if n != 1 {
		t.Fatal("missing coverage gap")
	}
	if e = f.app.DB.Exec("CREATE TRIGGER fixture_capture_failure BEFORE INSERT ON analytics_outbox BEGIN SELECT RAISE(ABORT,'fixture failure'); END").Error; e != nil {
		t.Fatal(e)
	}
	run("save-3", false)
	_ = f.app.SQL.QueryRow("SELECT count(*) FROM analytics_outbox").Scan(&n)
	if n != 1 {
		t.Fatal("failed capture leaked")
	}
}
