package analytics_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/analytics"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func TestAppOnlyRestoreRetainsCleanupLookup(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	life, e := f.app.InitializeLifecycleJournal(ctx, f.app.Path+".lifecycle")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = life.Close() }()
	if e = f.app.DB.Create(&store.Session{Hash: "restore-session", UserID: &f.user, ExpiresUnix: f.now.Add(time.Hour).Unix()}).Error; e != nil {
		t.Fatal(e)
	}
	p, e := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web")
	if e != nil {
		t.Fatal(e)
	}
	c := f.s.Request(ctx, f.user, p.Revision, "web")
	if e = f.app.DB.WithContext(c).Transaction(func(tx *gorm.DB) error { return analytics.CaptureEvent(c, tx, f.user, "meal_plan_saved", "saved-plan") }); e != nil {
		t.Fatal(e)
	}
	if e = f.s.Deliver(ctx, nil); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Refresh(ctx); e != nil {
		t.Fatal(e)
	}
	backup := filepath.Join(t.TempDir(), "app-backup.sqlite")
	if e = f.app.Backup(ctx, backup); e != nil {
		t.Fatal(e)
	}
	check, e := store.OpenReadOnly(backup)
	if e != nil {
		t.Fatal(e)
	}
	var n int
	for _, table := range []string{"analytics_preferences", "analytics_receipts", "analytics_outbox", "analytics_generation"} {
		_ = check.SQL.QueryRow("SELECT count(*) FROM " + table).Scan(&n)
		if n != 0 {
			t.Fatalf("backup retained %s", table)
		}
	}
	_ = check.Close()
	_ = f.s.Close()
	destination := filepath.Join(t.TempDir(), "restored.sqlite")
	if e = store.Restore(ctx, backup, destination, store.AccountRestoreOptions{JournalPath: life.Path}); e == nil {
		t.Fatal("restore accepted missing privacy journal")
	}
	opts := f.opts
	opts.Create = false
	if e = store.Restore(ctx, backup, destination, store.AccountRestoreOptions{JournalPath: life.Path, Analytics: &opts}); e != nil {
		t.Fatal(e)
	}
	app, e := store.Open(destination, false)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = app.Close() }()
	service, e := analytics.Open(ctx, app.DB, opts)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = service.Close() }()
	app.Analytics = service
	p, e = service.Preference(ctx, f.user)
	if e != nil || p.Enabled {
		t.Fatal("restored capture enabled", e)
	}
	data, release, e := service.PersonalExportLease(ctx, f.user)
	if e != nil {
		t.Fatal(e)
	}
	release()
	if !bytes.Contains(data, []byte("meal_plan_saved")) {
		t.Fatal("retained subject lookup lost")
	}
	actor := store.AccountActor{UserID: f.user, SessionHash: "restore-session", PasswordHash: "hash"}
	var zip bytes.Buffer
	if e = app.WriteAccountExport(ctx, actor, &zip, *f.now); e != nil {
		t.Fatal(e)
	}
	paired, e := app.OpenLifecycleJournal(ctx, life.Path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = paired.Close() }()
	if _, e = paired.DeleteAccount(ctx, actor, "11111111111111111111111111111111", "DELETE", *f.now, store.AccountDeletionOptions{}); e != nil {
		t.Fatal(e)
	}
	if e = service.Maintain(ctx); e != nil {
		t.Fatal(e)
	}
}
