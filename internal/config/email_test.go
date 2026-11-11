package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmailRejectsNoReplyAddresses(t *testing.T) {
	t.Setenv("MYCHEF_EMAIL_MODE", "resend")
	path := filepath.Join(t.TempDir(), "credentials.json")
	t.Setenv("MYCHEF_CREDENTIALS", path)
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	for _, tc := range []struct{ name, from, replyTo string }{
		{"valid", "MyChef <hello@example.test>", "support@example.test"},
		{"from", "MyChef <No-Reply@example.test>", ""},
		{"alias", "noreply+mychef@example.test", ""},
		{"reply-to", "hello@example.test", "No reply <no_reply@example.test>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(map[string]string{
				"resend_api_key": "re_fake_test_key", "email_from": tc.from, "email_reply_to": tc.replyTo,
				"resend_webhook_secret": "whsec_" + key, "email_fingerprint_key": key,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			_, err = LoadEmail()
			if tc.name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "no-reply") {
				t.Fatal("no-reply setting was not rejected")
			}
		})
	}
}

func TestEmailModes(t *testing.T) {
	t.Setenv("MYCHEF_DEV", "")
	t.Setenv("MYCHEF_EMAIL_MODE", "")
	t.Setenv("MYCHEF_CREDENTIALS", "")
	if _, err := LoadEmail(); err == nil {
		t.Fatal("production mode accepted missing credentials")
	}
	t.Setenv("MYCHEF_EMAIL_MODE", "fake")
	if _, err := LoadEmail(); err == nil {
		t.Fatal("fake mode accepted outside development")
	}
	t.Setenv("MYCHEF_DEV", "1")
	cfg, err := LoadEmail()
	if err != nil || !cfg.Fake {
		t.Fatalf("explicit development fake: %v", err)
	}
}

func TestEmailDisabledDoesNotRequireOrReadProviderCredentials(t *testing.T) {
	t.Setenv("MYCHEF_EMAIL_MODE", "disabled")
	t.Setenv("MYCHEF_DEV", "")
	t.Setenv("MYCHEF_CREDENTIALS", filepath.Join(t.TempDir(), "does-not-exist.json"))
	cfg, err := LoadEmail()
	if err != nil || !cfg.Disabled || cfg.Fake || cfg.APIKey != "" || cfg.From != "" {
		t.Fatal("explicitly disabled email required provider credentials", err)
	}
}
