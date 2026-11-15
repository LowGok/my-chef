package analytics_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/analytics"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

// Rehearse an already migrated disposable installation. The public-source run
// also exercises the real newsletter tables; an older schema cannot claim that.
func TestDisabledRollbackAndRecoveryRehearsal(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	const canary = "REHEARSAL_PRIVATE_PREFERENCE_7391"
	check(f.app.DB.Model(&store.User{}).Where("user_id=?", f.user).Update("preferences_json", `{"diet":["`+canary+`"]}`).Error)
	other := store.User{Email: "rehearsal-unrelated@example.test", Username: "unrelated", FirstName: "Unrelated", LastName: "Fixture", Password: "unrelated-password-hash"}
	check(f.app.DB.Create(&other).Error)
	var newsletter bool
	check(f.app.SQL.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='newsletter_subscriptions')").Scan(&newsletter))
	if newsletter {
		_, err := f.app.SQL.Exec(`INSERT INTO newsletter_subscriptions(user_id,email_key,status,locale,consent_version,source,requested_unix,revision) VALUES(?,?,'unsubscribed','nl-BE','rehearsal-v1','rehearsal',?,3)`, other.ID, other.Email, f.now.Unix())
		check(err)
	}
	preserve := func() {
		t.Helper()
		var preference, email, password string
		check(f.app.SQL.QueryRow("SELECT preferences_json FROM users WHERE user_id=?", f.user).Scan(&preference))
		if !bytes.Contains([]byte(preference), []byte(canary)) {
			t.Fatal("operational preference changed")
		}
		check(f.app.SQL.QueryRow("SELECT email,password FROM users WHERE user_id=?", other.ID).Scan(&email, &password))
		if email != other.Email || password != other.Password {
			t.Fatal("unrelated account changed")
		}
		if newsletter {
			var status, locale string
			var revision int
			check(f.app.SQL.QueryRow("SELECT status,locale,revision FROM newsletter_subscriptions WHERE user_id=?", other.ID).Scan(&status, &locale, &revision))
			if status != "unsubscribed" || locale != "nl-BE" || revision != 3 {
				t.Fatal("newsletter refusal changed")
			}
		}
	}
	p, err := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web")
	check(err)
	capture := f.s.Request(ctx, f.user, p.Revision, "web")
	check(f.app.DB.WithContext(capture).Transaction(func(tx *gorm.DB) error {
		return analytics.CaptureEvent(capture, tx, f.user, "meal_plan_saved", "completed-rehearsal-plan")
	}))
	interrupted := errors.New("disposable delivery interrupted")
	err = f.s.Deliver(ctx, func(stage string) error {
		if stage == "journal_committed" {
			return interrupted
		}
		return nil
	})
	if !errors.Is(err, interrupted) {
		t.Fatal("delivery interruption was not exercised")
	}
	check(f.s.Deliver(ctx, nil))
	extract, err := f.s.Refresh(ctx)
	check(err)
	read := func(path, query string) int64 {
		t.Helper()
		db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
		check(err)
		defer func() { _ = db.Close() }()
		var value int64
		check(db.QueryRow(query).Scan(&value))
		return value
	}
	reporting := read(extract, "SELECT reporting_unix FROM analytics_schema")
	if read(extract, "SELECT count(*) FROM activity_events") != 1 {
		t.Fatal("delivery retry duplicated or lost committed action")
	}
	for _, path := range []string{extract, filepath.Join(f.opts.Root, "journal.sqlite")} {
		raw, err := os.ReadFile(path)
		check(err)
		for _, value := range []string{canary, other.Email, other.Password, "fixture@example.invalid", "completed-rehearsal-plan"} {
			if bytes.Contains(raw, []byte(value)) {
				t.Fatal("operational canary entered analytical storage")
			}
		}
	}
	backup := filepath.Join(f.opts.Root, "rehearsal.enc")
	check(f.s.Backup(ctx, extract, backup))
	raw, err := os.ReadFile(backup)
	check(err)
	if bytes.Contains(raw, []byte("SQLite format")) || bytes.Contains(raw, []byte(canary)) {
		t.Fatal("recovery envelope exposed plaintext")
	}
	deadline := read(f.opts.PrivacyPath, "SELECT deadline FROM copies WHERE kind='backup'")
	if deadline != f.now.Add(7*24*time.Hour).Unix() {
		t.Fatal("backup deadline is not original seven-day ceiling")
	}
	p, err = f.s.Update(ctx, f.user, analytics.PreferenceUpdate{NoticeVersion: "core-v1", ExpectedRevision: p.Revision}, "web")
	check(err)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err = f.s.Maintain(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled maintenance did not stop")
	}
	// A compatible rollback keeps privacy service, journal, anchor and key. Only
	// collection is disabled; removing its configuration is not a safe rollback.
	check(f.s.Close())
	opts := f.opts
	opts.Create = false
	opts.Config.Enabled = false
	rolled, err := analytics.Open(ctx, f.app.DB, opts)
	check(err)
	defer func() { _ = rolled.Close() }()
	f.app.Analytics = rolled
	check(rolled.Maintain(ctx))
	if _, err = rolled.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1", ExpectedRevision: p.Revision}, "web"); !errors.Is(err, analytics.ErrDisabled) {
		t.Fatal("disabled rollback enabled capture")
	}
	if read(extract, "SELECT reporting_unix FROM analytics_schema") != reporting {
		t.Fatal("privacy cleanup moved original reporting date")
	}
	if read(extract, "SELECT count(*) FROM activity_events") != 0 || read(extract, "SELECT count(*) FROM monthly_contributions") != 0 {
		t.Fatal("withdrawn history remained in frozen extract")
	}
	p, err = rolled.Preference(ctx, f.user)
	check(err)
	if p.Enabled || p.DeletionStatus != "backup_pending" {
		t.Fatal("isolated recovery cleanup was not reported pending")
	}
	preserve()
	*f.now = f.now.Add(6 * 24 * time.Hour)
	restored := filepath.Join(f.opts.Root, "rehearsal-restored.sqlite")
	check(rolled.Restore(ctx, backup, restored))
	if read(restored, "SELECT reporting_unix FROM analytics_schema") != reporting || read(restored, "SELECT count(*) FROM activity_events") != 0 {
		t.Fatal("restore changed reporting date or resurrected withdrawn history")
	}
	if read(f.opts.PrivacyPath, "SELECT deadline FROM copies WHERE kind='backup'") != deadline {
		t.Fatal("restore extended original recovery deadline")
	}
	*f.now = time.Unix(deadline+1, 0).UTC()
	check(rolled.Maintain(ctx))
	if _, err = os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired isolated recovery retained")
	}
	p, err = rolled.Preference(ctx, f.user)
	check(err)
	if p.DeletionStatus != "complete" {
		t.Fatal("cleanup remained pending after original backup expiry")
	}
	*f.now = f.now.Add(-time.Hour)
	if err = rolled.Maintain(ctx); err == nil {
		t.Fatal("backward retention clock accepted")
	}
	*f.now = time.Unix(deadline+2, 0).UTC()
	check(rolled.Maintain(ctx))
	preserve()
	if newsletter {
		t.Log("real newsletter table refusal preserved")
	} else {
		t.Log("newsletter preservation unavailable in this older source schema; public-source run required")
	}
	t.Log("disabled compatible reopen, interrupted delivery/maintenance, frozen reporting date, sanitized restore, original expiry, canaries and unrelated operational data passed")
}
