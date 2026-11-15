package analytics_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/analytics"
	"gorm.io/gorm"
)

func TestOptoutBackupRestoreAndReportLease(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, e := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web")
	if e != nil {
		t.Fatal(e)
	}
	c := f.s.Request(ctx, f.user, p.Revision, "web")
	if e = f.app.DB.WithContext(c).Transaction(func(tx *gorm.DB) error { return analytics.CaptureEvent(c, tx, f.user, "meal_plan_saved", "plan") }); e != nil {
		t.Fatal(e)
	}
	if e = f.s.Deliver(ctx, nil); e != nil {
		t.Fatal(e)
	}
	path, e := f.s.Refresh(ctx)
	if e != nil {
		t.Fatal(e)
	}
	backup := filepath.Join(f.opts.Root, "recovery.enc")
	if e = f.s.Backup(ctx, path, backup); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(backup)
	if e != nil || bytes.Contains(raw, []byte("SQLite format")) {
		t.Fatal("unencrypted backup", e)
	}
	p, e = f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: false, NoticeVersion: "core-v1", ExpectedRevision: p.Revision}, "web")
	if e != nil || p.Enabled {
		t.Fatal(e)
	}
	if e = f.s.Maintain(ctx); e != nil {
		t.Fatal(e)
	}
	export, release, e := f.s.PersonalExportLease(ctx, f.user)
	if e != nil {
		t.Fatal(e)
	}
	release()
	if bytes.Contains(export, []byte("meal_plan_saved")) {
		t.Fatal("erased event exported")
	}
	restored := filepath.Join(f.opts.Root, "restored.sqlite")
	if e = f.s.Restore(ctx, backup, restored); e != nil {
		t.Fatal(e)
	}
	var report bytes.Buffer
	if e = f.s.WithReport(ctx, restored, f.now.Add(-3600).Unix(), f.now.Unix()+1, &report); e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(report.Bytes(), []byte("sub_")) {
		t.Fatal("subject in report")
	}

	*f.now = f.now.Add(7*24*time.Hour + time.Second)
	if e = f.s.Maintain(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(backup); !os.IsNotExist(e) {
		t.Fatal("expired backup retained")
	}
	if e = f.s.Restore(ctx, backup, filepath.Join(f.opts.Root, "late.sqlite")); e == nil {
		t.Fatal("expired recovery accepted")
	}
}

func TestReportLeasePrecedesOptoutAcknowledgement(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, e := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web")
	if e != nil {
		t.Fatal(e)
	}
	path, e := f.s.Refresh(ctx)
	if e != nil {
		t.Fatal(e)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	reported := make(chan error, 1)
	go func() {
		reported <- f.s.WithReport(ctx, path, f.now.Add(-3600).Unix(), f.now.Unix()+1, &blockingOutput{entered: entered, release: release})
	}()
	<-entered
	off := make(chan error, 1)
	go func() {
		_, e := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: false, NoticeVersion: "core-v1", ExpectedRevision: p.Revision}, "web")
		off <- e
	}()
	select {
	case e := <-off:
		t.Fatal("off acknowledged while report output held lease", e)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if e = <-reported; e != nil {
		t.Fatal(e)
	}
	if e = <-off; e != nil {
		t.Fatal(e)
	}
}

func TestErasureReclaimsEventBytes(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, e := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web")
	if e != nil {
		t.Fatal(e)
	}
	c := f.s.Request(ctx, f.user, p.Revision, "web")
	if e = f.app.DB.WithContext(c).Transaction(func(tx *gorm.DB) error {
		return analytics.CaptureEvent(c, tx, f.user, "meal_plan_saved", "byte-canary")
	}); e != nil {
		t.Fatal(e)
	}
	var id string
	if e = f.app.SQL.QueryRow("SELECT id FROM analytics_outbox").Scan(&id); e != nil {
		t.Fatal(e)
	}
	if e = f.s.Deliver(ctx, nil); e != nil {
		t.Fatal(e)
	}
	path, e := f.s.Refresh(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: false, NoticeVersion: "core-v1", ExpectedRevision: p.Revision}, "web"); e != nil {
		t.Fatal(e)
	}
	if e = f.s.Maintain(ctx); e != nil {
		t.Fatal(e)
	}
	for _, file := range []string{path, filepath.Join(f.opts.Root, "journal.sqlite"), f.app.Path, f.app.Path + "-wal"} {
		b, e := os.ReadFile(file)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(b, []byte(id)) {
			t.Fatalf("erased event survives in %s", filepath.Base(file))
		}
	}
}

func TestOperatorAndAppSharePrivacyLease(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, e := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web")
	if e != nil {
		t.Fatal(e)
	}
	path, e := f.s.Refresh(ctx)
	if e != nil {
		t.Fatal(e)
	}
	options := f.opts
	options.Create = false
	peer, e := analytics.Open(ctx, f.app.DB, options)
	if e != nil {
		t.Fatal("operator cannot open while app runs", e)
	}
	defer func() { _ = peer.Close() }()
	entered := make(chan struct{})
	release := make(chan struct{})
	report := make(chan error, 1)
	go func() {
		report <- f.s.WithReport(ctx, path, f.now.Add(-time.Hour).Unix(), f.now.Unix()+1, &blockingOutput{entered: entered, release: release})
	}()
	<-entered
	off := make(chan error, 1)
	go func() {
		_, e := peer.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: false, NoticeVersion: "core-v1", ExpectedRevision: p.Revision}, "web")
		off <- e
	}()
	select {
	case e := <-off:
		t.Fatal("cross-process lease did not fence report", e)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if e = <-report; e != nil {
		t.Fatal(e)
	}
	if e = <-off; e != nil {
		t.Fatal(e)
	}
}

func TestMissingCleanupLookupFailsIndependentAnchor(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, e := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web"); e != nil {
		t.Fatal(e)
	}
	_ = f.s.Close()
	db, e := sql.Open("sqlite3", f.opts.PrivacyPath)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("DELETE FROM links"); e != nil {
		t.Fatal(e)
	}
	_ = db.Close()
	o := f.opts
	o.Create = false
	if s, e := analytics.Open(ctx, f.app.DB, o); e == nil {
		_ = s.Close()
		t.Fatal("missing cleanup lookup accepted")
	}
}

func TestUnrelatedBackupDoesNotDelayWithdrawalCompletion(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, err := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web")
	if err != nil {
		t.Fatal(err)
	}
	path, err := f.s.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(f.opts.Root, "old.enc")
	if err = f.s.Backup(ctx, path, old); err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: false, NoticeVersion: "core-v1", ExpectedRevision: p.Revision}, "web")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Preference(ctx, f.user)
	if err != nil || p.DeletionStatus != "backup_pending" {
		t.Fatalf("pending %+v %v", p, err)
	}
	*f.now = f.now.Add(6 * 24 * time.Hour)
	path, err = f.s.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(f.opts.Root, "unrelated.enc")
	if err = f.s.Backup(ctx, path, unrelated); err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(24*time.Hour + time.Second)
	if err = f.s.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	p, err = f.s.Preference(ctx, f.user)
	if err != nil || p.DeletionStatus != "complete" {
		t.Fatalf("completed %+v %v", p, err)
	}
	if _, err = os.Stat(unrelated); err != nil {
		t.Fatal("unrelated backup should remain", err)
	}
}

func TestCancelledPrivacyWaitDoesNotWaitForReader(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web"); err != nil {
		t.Fatal(err)
	}
	path, err := f.s.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- f.s.WithReport(ctx, path, f.now.Add(-time.Hour).Unix(), f.now.Unix()+1, &blockingOutput{entered: entered, release: release})
	}()
	<-entered
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	waited := make(chan error, 1)
	go func() { _, err := f.s.Preference(cancelled, f.user); waited <- err }()
	select {
	case err := <-waited:
		if err == nil {
			t.Error("cancelled lock wait succeeded")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("cancelled privacy lock wait remained blocked")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

// This boundary stops writing before its cancelled call returns.
type blockingOutput struct {
	entered chan struct{}
	release chan struct{}
}

func (w *blockingOutput) Write([]byte) (int, error) { return 0, errors.New("context output required") }
func (w *blockingOutput) WriteContext(ctx context.Context, b []byte) (int, error) {
	close(w.entered)
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-w.release:
		return len(b), nil
	}
}
func TestCancelledReportOutputReleasesFence(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, err := f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web")
	if err != nil {
		t.Fatal(err)
	}
	path, err := f.s.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	reading, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- f.s.WithReport(reading, path, f.now.Add(-time.Hour).Unix(), f.now.Unix()+1, &blockingOutput{entered, release})
	}()
	<-entered
	cancel()
	if err = <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled output", err)
	}
	// Probe the released process fence directly, before timing any SQLite work.
	lock, err := os.OpenFile(f.opts.PrivacyPath+".lock", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal("cancelled report retained process fence", err)
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	// Preference also acquires the in-process fence, without doing erasure I/O.
	probe, finish := context.WithTimeout(ctx, time.Second)
	_, err = f.s.Preference(probe, f.user)
	finish()
	if err != nil {
		t.Fatal("cancelled report retained service fence", err)
	}
	// Withdrawal and maintenance include durable SQLite writes. Their deadline
	// bounds a stuck operation; it is not a one-second disk-speed requirement.
	off, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	if _, err = f.s.Update(off, f.user, analytics.PreferenceUpdate{NoticeVersion: "core-v1", ExpectedRevision: p.Revision}, "web"); err != nil {
		t.Fatal(err)
	}
	// The one-second check above proves the cancelled reader released its fence.
	// Give physical cleanup its own bounded context rather than the unused
	// remainder of the preference update's deadline.
	cleanup, finishCleanup := context.WithTimeout(ctx, 5*time.Second)
	defer finishCleanup()
	if err = f.s.Maintain(cleanup); err != nil {
		t.Fatal(err)
	}
}
func TestBlockedPipeOutputHonorsDeadline(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (analytics.OutputWriter{Context: ctx, Output: writer}).Write(bytes.Repeat([]byte("x"), 1<<20))
		done <- err
	}()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("blocked output completed")
		}
	case <-time.After(time.Second):
		t.Fatal("pipe deadline did not interrupt write")
	}
}
