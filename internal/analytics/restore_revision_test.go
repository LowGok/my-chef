package analytics_test

import (
	"context"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/analytics"
	"gorm.io/gorm"
)

func TestRestoreAdvancesRevisionAndRejectsOldOfflineAction(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, e := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web")
	if e != nil {
		t.Fatal(e)
	}
	old := p.Revision
	*f.now = f.now.Add(time.Second)
	if e = f.s.ReconcileAppRestore(ctx); e != nil {
		t.Fatal(e)
	}
	p, e = f.s.Preference(ctx, f.user)
	if e != nil || p.Enabled || p.Revision <= old {
		t.Fatalf("restored revision was reused: %+v %v", p, e)
	}
	p, e = f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1", ExpectedRevision: p.Revision}, "web")
	if e != nil {
		t.Fatal(e)
	}
	c := f.s.Request(ctx, f.user, old, "web")
	if e = f.app.DB.WithContext(c).Transaction(func(tx *gorm.DB) error {
		return analytics.CaptureEvent(c, tx, f.user, "favorite_saved", "queued-before-restore")
	}); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = f.app.SQL.QueryRow("SELECT count(*) FROM analytics_outbox").Scan(&n); e != nil || n != 0 {
		t.Fatal("old offline action captured", n, e)
	}
}
func TestRestorePreservesObjectionControl(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, e := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: false, NoticeVersion: "core-v1"}, "web")
	if e != nil {
		t.Fatal(e)
	}
	*f.now = f.now.Add(time.Second)
	if e = f.s.ReconcileAppRestore(ctx); e != nil {
		t.Fatal(e)
	}
	got, e := f.s.Preference(ctx, f.user)
	if e != nil || got.Enabled || got.Source != "objection" || got.Revision <= p.Revision {
		t.Fatalf("objection lost %+v %v", got, e)
	}
}
