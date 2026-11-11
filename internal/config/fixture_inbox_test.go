package config

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFixtureInboxRequiresAnExplicitIsolatedRuntime(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	file := filepath.Join(t.TempDir(), "fixture.json")
	capability := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	write := func(until time.Time) {
		t.Helper()
		data := fmt.Sprintf(`{"capability":%q,"expires":%q}`, capability, until.Format(time.RFC3339))
		if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MYCHEF_DEV", "1")
	t.Setenv("MYCHEF_E2E", "1")
	t.Setenv("MYCHEF_FIXTURE_INBOX_FILE", "")
	cfg := Config{Addr: "127.0.0.1:8080", Origin: "https://preview.example.test", Provider: "fixture"}
	email := Email{Fake: true}
	if settings, err := LoadFixtureInbox(cfg, email, now); err != nil || settings != nil {
		t.Fatal("fixture inbox must be off by default")
	}
	t.Setenv("MYCHEF_FIXTURE_INBOX_FILE", file)
	write(now.Add(10 * time.Minute))
	if settings, err := LoadFixtureInbox(cfg, email, now); err != nil || settings == nil || settings.AllowLoopbackLinks {
		t.Fatal("HTTPS fixture inbox rejected or gained a loopback exception")
	}
	local := cfg
	local.Origin = "http://127.0.0.1:8080"
	if settings, err := LoadFixtureInbox(local, email, now); err != nil || settings == nil || !settings.AllowLoopbackLinks {
		t.Fatal("loopback HTTP not restricted to an explicitly enabled fixture")
	}
	unsafe := []struct {
		cfg   Config
		email Email
	}{
		{Config{Addr: "0.0.0.0:8080", Origin: cfg.Origin, Provider: "fixture"}, email},
		{Config{Addr: cfg.Addr, Origin: "http://preview.example.test", Provider: "fixture"}, email},
		{Config{Addr: cfg.Addr, Origin: cfg.Origin, Provider: "gemini"}, email},
		{cfg, Email{Fake: false}},
	}
	for _, tc := range unsafe {
		if settings, err := LoadFixtureInbox(tc.cfg, tc.email, now); err == nil || settings != nil {
			t.Fatal("an unsafe runtime enabled a fixture inbox")
		}
	}
	for _, setting := range []string{"MYCHEF_DEV", "MYCHEF_E2E"} {
		t.Run(setting+" required", func(t *testing.T) {
			t.Setenv(setting, "0")
			write(now.Add(10 * time.Minute))
			if settings, err := LoadFixtureInbox(cfg, email, now); err == nil || settings != nil {
				t.Fatal("fixture enabled without explicit isolated development flags")
			}
		})
	}
	write(now.Add(31 * time.Minute))
	if _, err := LoadFixtureInbox(cfg, email, now); err == nil {
		t.Fatal("overlong fixture capability accepted")
	}
}
