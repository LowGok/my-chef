package analytics_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/analytics"
	"github.com/LowGok/my-chef/internal/store"
)

type fixture struct {
	s    *analytics.Service
	app  *store.Store
	now  *time.Time
	user int64
	opts analytics.OpenOptions
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	app, e := store.Open(filepath.Join(root, "app.sqlite"), true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = app.Close() })
	if e = app.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	stamp := now.Unix()
	u := store.User{Email: "fixture@example.invalid", Username: "fixture", FirstName: "Fixture", LastName: "Only", Password: "hash", EmailVerifiedAt: &stamp}
	if e = app.DB.Create(&u).Error; e != nil {
		t.Fatal(e)
	}
	cfg := analytics.Config{Enabled: true, NoticeVersion: "core-v1", NoticeHash: analytics.Digest([]byte(analytics.NoticeText)), OutboxCap: 100, Assessment: analytics.Assessment{Status: "approved", Basis: "legitimate_interest", Version: "fixture-assessment", Scope: analytics.ScopeDigest(), NoticeVersion: "core-v1", Jurisdiction: "BE-fixture", Audience: "fictional", DeviceAccess: "fictional-reviewed", ValidUntil: now.AddDate(5, 0, 0).Unix()}}
	o := analytics.OpenOptions{Root: filepath.Join(root, "analytics"), PrivacyPath: filepath.Join(root, "privacy.sqlite"), AnchorPath: filepath.Join(root, "privacy.anchor"), Create: true, BackupKey: bytes.Repeat([]byte{7}, 32), Config: cfg, Now: func() time.Time { return now }}
	s, e := analytics.Open(ctx, app.DB, o)
	if e != nil {
		t.Fatal(e)
	}
	app.Analytics = s
	t.Cleanup(func() { _ = s.Close() })
	return fixture{s, app, &now, u.ID, o}
}
func TestPreferenceLifecycle(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, e := f.s.Preference(ctx, f.user)
	if e != nil || p.Enabled || p.Revision != 0 {
		t.Fatalf("migration: %+v %v", p, e)
	}
	p, e = f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1", ExpectedRevision: 0}, "web")
	if e != nil || !p.Enabled || p.Source != "explicit_enable" {
		t.Fatalf("enable: %+v %v", p, e)
	}
	if _, e = f.s.Update(ctx, f.user, analytics.PreferenceUpdate{NoticeVersion: "core-v1", ExpectedRevision: 0}, "web"); !errors.Is(e, analytics.ErrConflict) {
		t.Fatalf("stale: %v", e)
	}
	p, e = f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: false, NoticeVersion: "core-v1", ExpectedRevision: 1}, "web")
	if e != nil || p.Enabled || p.Source != "objection" {
		t.Fatalf("off: %+v %v", p, e)
	}
	p, e = f.s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1", ExpectedRevision: 2}, "ios")
	if e != nil || !p.Enabled || p.Revision != 3 {
		t.Fatalf("re-enable: %+v %v", p, e)
	}
	var n int
	if e = f.app.SQL.QueryRow("SELECT count(DISTINCT subject) FROM analytics_preferences").Scan(&n); e != nil || n != 1 {
		t.Fatal(e)
	}
}
func TestPrivacyAnchorAndBindingFailClosed(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_ = f.s.Close()
	o := f.opts
	o.Create = false
	before, e := os.ReadFile(o.AnchorPath)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(o.AnchorPath, []byte("old"), 0600); e != nil {
		t.Fatal(e)
	}
	if s, e := analytics.Open(ctx, f.app.DB, o); e == nil {
		_ = s.Close()
		t.Fatal("stale anchor accepted")
	}
	_ = os.WriteFile(o.AnchorPath, before, 0600)
	o.Config.Enabled = false
	s, e := analytics.Open(ctx, f.app.DB, o)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	if _, e = s.Update(ctx, f.user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "core-v1"}, "web"); !errors.Is(e, analytics.ErrDisabled) {
		t.Fatal(e)
	}
}
