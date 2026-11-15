package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewsletterFixtureAndReadiness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(`{"newsletter_service_token":"`+strings.Repeat("s", 64)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"MYCHEF_NEWSLETTER_MODE": "fixture", "MYCHEF_DEV": "1", "MYCHEF_CREDENTIALS": path, "MYCHEF_NEWSLETTER_CONTACT": "preview@example.test", "MYCHEF_NEWSLETTER_PENDING_DAYS": "7", "MYCHEF_NEWSLETTER_WITHDRAWN_DAYS": "30", "MYCHEF_NEWSLETTER_OPERATIONS_READY": "1"} {
		t.Setenv(key, value)
	}
	app := Config{Addr: "127.0.0.1:18345", Provider: "fixture"}
	settings, err := LoadNewsletter(app, Email{Fake: true})
	if err != nil {
		t.Fatal(err)
	}
	checks := settings.Readiness(true, true, true, false)
	for name, ready := range checks {
		if !ready {
			t.Fatalf("fixture prerequisite %s missing", name)
		}
	}
	settings.Mode = "enabled"
	if settings.Readiness(true, true, true, false)["identityCompatibility"] {
		t.Fatal("production bypassed missing app compatibility")
	}
	for _, change := range []func(*Newsletter){func(c *Newsletter) { c.ServiceToken = "" }, func(c *Newsletter) { c.Contact = "" }, func(c *Newsletter) { c.PendingDays = 0 }, func(c *Newsletter) { c.WithdrawnDays = 0 }, func(c *Newsletter) { c.OperationsReady = false }} {
		candidate := settings
		change(&candidate)
		ready := true
		for _, value := range candidate.Readiness(true, true, true, true) {
			ready = ready && value
		}
		if ready {
			t.Fatal("missing prerequisite accepted")
		}
	}
	t.Setenv("MYCHEF_DEV", "0")
	if _, err := LoadNewsletter(app, Email{Fake: true}); err == nil {
		t.Fatal("fixture accepted outside development")
	}
	t.Setenv("MYCHEF_DEV", "1")
	app.Addr = "0.0.0.0:18345"
	if _, err := LoadNewsletter(app, Email{Fake: true}); err == nil {
		t.Fatal("fixture accepted on public listener")
	}
}

func TestNewsletterDisabledDefault(t *testing.T) {
	t.Setenv("MYCHEF_NEWSLETTER_MODE", "")
	settings, err := LoadNewsletter(Config{}, Email{})
	if err != nil || settings.Mode != "disabled" {
		t.Fatal("default capture is enabled", err)
	}
}
