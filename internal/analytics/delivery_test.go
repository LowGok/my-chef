package analytics_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/LowGok/my-chef/internal/analytics"
	"gorm.io/gorm"
)

func TestDeliveryCrashAndRegisteredRefresh(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, e := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web")
	if e != nil {
		t.Fatal(e)
	}
	ctx = f.s.Request(ctx, f.user, p.Revision, "web")
	if e = f.app.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return analytics.CaptureEvent(ctx, tx, f.user, "meal_plan_saved", "committed-plan")
	}); e != nil {
		t.Fatal(e)
	}
	crash := errors.New("fixture crash")
	if e = f.s.Deliver(ctx, func(stage string) error {
		if stage == "journal_committed" {
			return crash
		}
		return nil
	}); !errors.Is(e, crash) {
		t.Fatal(e)
	}
	if e = f.s.Deliver(ctx, nil); e != nil {
		t.Fatal(e)
	}
	var n int
	_ = f.app.SQL.QueryRow("SELECT count(*) FROM analytics_outbox").Scan(&n)
	if n != 0 {
		t.Fatal("ack payload retained")
	}
	path, e := f.s.Refresh(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var report bytes.Buffer
	if e = f.s.WithReport(ctx, path, f.now.Add(-3600).Unix(), f.now.Unix()+1, &report); e != nil {
		t.Fatal(e)
	}
	if report.Len() == 0 {
		t.Fatal("empty report")
	}

	if e = f.s.WithReport(ctx, f.app.Path, f.now.Add(-3600).Unix(), f.now.Unix()+1, &bytes.Buffer{}); !errors.Is(e, analytics.ErrPrivacy) {
		t.Fatalf("unregistered report: %v", e)
	}
}
