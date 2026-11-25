package main

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/config"
)

func TestBillingFixtureRequiresAnExplicitDisposableLoopbackSetup(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	app := config.Config{Provider: "fixture", Addr: "127.0.0.1:8000", Origin: "http://127.0.0.1:8000",
		Database: filepath.Join(root, "app.db"), LifecycleDatabase: filepath.Join(root, "app.db.lifecycle")}
	values := &config.FixtureInbox{Origin: app.Origin, Expires: time.Now().Add(time.Minute), CapabilityHash: [32]byte{1}}

	t.Setenv("MYCHEF_BILLING_FIXTURE", "")
	if mode, err := loadBillingFixtureMode(app, values); err != nil || mode != nil {
		t.Fatal("billing fixture was enabled by default", err)
	}
	t.Setenv("MYCHEF_BILLING_FIXTURE", "1")
	if mode, err := loadBillingFixtureMode(app, values); err == nil || mode != nil {
		t.Fatal("fixture could open a database without the explicit disposable root", err)
	}
	t.Setenv("MYCHEF_BILLING_FIXTURE_ROOT", root)
	t.Setenv("MYCHEF_DEV", "1")
	t.Setenv("MYCHEF_E2E", "1")
	if mode, err := loadBillingFixtureMode(app, values); fixtureBillingFactory == nil && (err == nil || mode != nil) {
		t.Fatal("normal binary accepted a billing fixture", err)
	} else if fixtureBillingFactory != nil && (err != nil || mode == nil) {
		t.Fatal("fixture build did not accept the explicit disposable setup", err)
	}
	wrong := app
	wrong.Database = filepath.Join(root, "real.db")
	if mode, err := loadBillingFixtureMode(wrong, values); err == nil || mode != nil {
		t.Fatal("fixture could open a non-disposable database name", err)
	}
	wrong = app
	wrong.Origin = "https://public.example.test"
	if mode, err := loadBillingFixtureMode(wrong, values); err == nil || mode != nil {
		t.Fatal("fixture could run with a public origin", err)
	}
	if mode, err := loadBillingFixtureMode(app, nil); err == nil || mode != nil {
		t.Fatal("fixture accepted a missing fake-inbox lease", err)
	}
}

func TestBillingFixtureAllowsOnlyTheInstalledLoopbackHTTPSRelay(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYCHEF_BILLING_FIXTURE", "1")
	t.Setenv("MYCHEF_BILLING_FIXTURE_ROOT", root)
	t.Setenv("MYCHEF_DEV", "1")
	t.Setenv("MYCHEF_E2E", "1")
	base := config.Config{Provider: "fixture", Addr: "127.0.0.1:18137", Origin: "https://localhost:8977",
		Database: filepath.Join(root, "app.db"), LifecycleDatabase: filepath.Join(root, "app.db.lifecycle")}
	validInbox := config.FixtureInbox{Origin: base.Origin, Expires: time.Now().Add(time.Minute), CapabilityHash: [32]byte{1}}

	mode, err := loadBillingFixtureMode(base, &validInbox)
	if fixtureBillingFactory != nil && (err != nil || mode == nil) {
		t.Fatal("fixture build refused the installed loopback HTTPS relay", err)
	}
	if fixtureBillingFactory == nil && (err == nil || mode != nil) {
		t.Fatal("normal build accepted the installed relay")
	}

	for _, test := range []struct {
		name   string
		change func(*config.Config)
	}{
		{"alternate local port", func(c *config.Config) { c.Origin = "https://localhost:8978" }},
		{"alternate backend port", func(c *config.Config) { c.Addr = "127.0.0.1:18138" }},
		{"unencrypted relay", func(c *config.Config) { c.Origin = "http://localhost:8977" }},
		{"public hostname", func(c *config.Config) { c.Origin = "https://public.example.test:8977" }},
		{"wildcard listener", func(c *config.Config) { c.Addr = "0.0.0.0:18137" }},
		{"forwarded identity", func(c *config.Config) { c.TrustedProxies = []netip.Addr{netip.MustParseAddr("127.0.0.1")} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.change(&changed)
			inbox := validInbox
			inbox.Origin = changed.Origin
			if mode, err := loadBillingFixtureMode(changed, &inbox); err == nil || mode != nil {
				t.Fatal("fixture accepted another origin, listener or trusted proxy")
			}
		})
	}
}
