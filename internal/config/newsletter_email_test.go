package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func newsletterRoleCredential(t *testing.T, values map[string]string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "newsletter-credentials.json")
	body, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYCHEF_CREDENTIALS", path)
}
func newsletterRoleProfile() map[string]string {
	return map[string]string{
		"resend_api_key":            "re_fake_newsletter_role_key",
		"email_fingerprint_key":     base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"newsletter_email_from":     "MyChef <newsletter@mychef.dev>",
		"newsletter_email_reply_to": "feedback@gkldevelopment.com",
	}
}
func TestNewsletterRoleEmailNeedsOnlyItsOwnSendingProfile(t *testing.T) {
	t.Setenv("MYCHEF_EMAIL_MODE", "resend")
	t.Setenv("MYCHEF_DEV", "")
	newsletterRoleCredential(t, newsletterRoleProfile())
	cfg, err := LoadNewsletterEmail()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Fake || cfg.Disabled || cfg.NewsletterFrom != "MyChef <newsletter@mychef.dev>" || cfg.NewsletterReplyTo != "feedback@gkldevelopment.com" || len(cfg.FingerprintKey) != 32 || len(cfg.WebhookKey) != 0 {
		t.Fatal("role did not load its sending-only profile")
	}
	if _, err := LoadEmail(); err == nil {
		t.Fatal("full app stopped requiring its shared sender and webhook")
	}
}
func TestNewsletterRoleEmailRejectsMissingOrInvalidProfile(t *testing.T) {
	for _, tc := range []struct{ name, field, value string }{
		{"sender", "newsletter_email_from", ""}, {"reply", "newsletter_email_reply_to", ""},
		{"no-reply-sender", "newsletter_email_from", "no-reply@example.test"},
		{"no-reply-reply", "newsletter_email_reply_to", "no_reply@example.test"},
		{"key", "resend_api_key", "invalid"}, {"fingerprint", "email_fingerprint_key", "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MYCHEF_EMAIL_MODE", "resend")
			values := newsletterRoleProfile()
			values[tc.field] = tc.value
			values["email_from"] = "App <account@example.test>"
			values["email_reply_to"] = "help@example.test"
			newsletterRoleCredential(t, values)
			if _, err := LoadNewsletterEmail(); err == nil {
				t.Fatal("invalid role profile accepted")
			}
		})
	}
}
func TestNewsletterRoleEmailFakeIsExplicitDevelopmentOnly(t *testing.T) {
	t.Setenv("MYCHEF_EMAIL_MODE", "fake")
	t.Setenv("MYCHEF_DEV", "")
	values := newsletterRoleProfile()
	delete(values, "resend_api_key")
	delete(values, "email_fingerprint_key")
	newsletterRoleCredential(t, values)
	if _, err := LoadNewsletterEmail(); err == nil {
		t.Fatal("production accepted fake email")
	}
	t.Setenv("MYCHEF_DEV", "1")
	cfg, err := LoadNewsletterEmail()
	if err != nil || !cfg.Fake || cfg.APIKey != "" || cfg.NewsletterFrom != values["newsletter_email_from"] {
		t.Fatal("explicit fake profile unavailable", err)
	}
}
func TestNewsletterRoleEmailCannotStartDisabledOrUnconfigured(t *testing.T) {
	t.Setenv("MYCHEF_DEV", "")
	t.Setenv("MYCHEF_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
	for _, mode := range []string{"", "disabled", "unknown", "resend"} {
		t.Setenv("MYCHEF_EMAIL_MODE", mode)
		if _, err := LoadNewsletterEmail(); err == nil {
			t.Fatal("role accepted unavailable email configuration")
		}
	}
}
