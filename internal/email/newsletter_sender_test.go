package email

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/config"
)

func newsletterEmailConfig(t *testing.T, from, reply string) config.Email {
	t.Helper()
	t.Setenv("MYCHEF_EMAIL_MODE", "resend")
	path := filepath.Join(t.TempDir(), "credentials.json")
	t.Setenv("MYCHEF_CREDENTIALS", path)
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	data, _ := json.Marshal(map[string]string{"resend_api_key": "re_fake_test_key", "email_from": "Security <account@example.test>", "email_reply_to": "security@example.test", "resend_webhook_secret": "whsec_" + key, "email_fingerprint_key": key, "newsletter_email_from": from, "newsletter_email_reply_to": reply})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadEmail()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestNewsletterSenderProfilesKeepSecurityMailSeparate(t *testing.T) {
	for _, locale := range []string{"en", "fr-BE", "nl-BE"} {
		for _, transfer := range []bool{false, true} {
			t.Run(locale+map[bool]string{false: "/confirmation", true: "/transfer"}[transfer], func(t *testing.T) {
				db, _, now, keys := newsletterFixture(t, locale, transfer)
				cfg := newsletterEmailConfig(t, "MyChef <newsletter@mychef.dev>", "feedback@gkldevelopment.com")
				q := Queue{Store: db, ProofKeys: keys, Now: func() time.Time { return now }}
				svc := New(q, cfg)
				provider := &proofSender{result: deliveryResult{id: "newsletter-fake"}}
				svc.worker.sender = provider // No network: capture the actual outbound payload.
				if worked, err := svc.worker.Step(t.Context()); err != nil || !worked || len(provider.bodies) != 1 {
					t.Fatal("newsletter not rendered", worked, err)
				}
				var payload map[string]any
				if err := json.Unmarshal(provider.bodies[0], &payload); err != nil {
					t.Fatal(err)
				}
				if payload["from"] != "MyChef <newsletter@mychef.dev>" || payload["reply_to"] != "feedback@gkldevelopment.com" {
					t.Fatal("newsletter did not use its selected sender/reply profile")
				}
				if _, err := q.Enqueue(t.Context(), nil, Request{Key: "security-fixture", Recipient: "security-reader@example.test", Template: "provider-smoke-test/v1", Fields: map[string]string{"testID": "security-fixture", "timestamp": now.Format(time.RFC3339)}}); err != nil {
					t.Fatal(err)
				}
				provider.result = deliveryResult{id: "security-fake"}
				if _, err := svc.worker.Step(t.Context()); err != nil || len(provider.bodies) != 2 {
					t.Fatal("security fixture not sent", err)
				}
				payload = nil
				if err := json.Unmarshal(provider.bodies[1], &payload); err != nil {
					t.Fatal(err)
				}
				if payload["from"] != cfg.From || payload["reply_to"] != cfg.ReplyTo {
					t.Fatal("newsletter changed the shared security profile")
				}
			})
		}
	}
}

func TestNewsletterProfileLegacyAndOmittedReply(t *testing.T) {
	for _, from := range []string{"", "MyChef <newsletter@mychef.dev>"} {
		t.Run(from, func(t *testing.T) {
			db, _, now, keys := newsletterFixture(t, "en", false)
			cfg := newsletterEmailConfig(t, from, "")
			svc := New(Queue{Store: db, ProofKeys: keys, Now: func() time.Time { return now }}, cfg)
			provider := &proofSender{result: deliveryResult{id: "fake"}}
			svc.worker.sender = provider
			if _, err := svc.worker.Step(t.Context()); err != nil || len(provider.bodies) != 1 {
				t.Fatal("fixture not sent", err)
			}
			var body map[string]any
			if err := json.Unmarshal(provider.bodies[0], &body); err != nil {
				t.Fatal(err)
			}
			if from == "" {
				if body["from"] != cfg.From || body["reply_to"] != cfg.ReplyTo {
					t.Fatal("legacy profile changed")
				}
			} else {
				if body["from"] != from {
					t.Fatal("newsletter override ignored")
				}
				if _, ok := body["reply_to"]; ok {
					t.Fatal("omitted newsletter reply inherited shared Reply-To")
				}
			}
		})
	}
}

func TestNewsletterRetryBindsItsOwnSenderProfile(t *testing.T) {
	for _, change := range []string{"none", "sender", "reply-to", "shared-only"} {
		t.Run(change, func(t *testing.T) {
			db, initial, now, keys := newsletterFixture(t, "en", false)
			from, reply := "MyChef <newsletter@mychef.dev>", "feedback@gkldevelopment.com"
			cfg := newsletterEmailConfig(t, from, reply)
			q := Queue{Store: db, ProofKeys: keys, Now: func() time.Time { return now }}
			provider := &proofSender{result: deliveryResult{temporary: true}}
			svc := New(q, cfg)
			svc.worker.sender = provider
			if _, err := svc.worker.Step(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(provider.bodies) != 1 {
				t.Fatal("initial attempt missing")
			}
			now = now.Add(time.Minute)
			if change == "sender" {
				from = "MyChef <changed@example.test>"
			}
			if change == "reply-to" {
				reply = "changed@example.test"
			}
			cfg = newsletterEmailConfig(t, from, reply)
			if change == "shared-only" {
				cfg.From = "Shared <other@example.test>"
				cfg.ReplyTo = "other@example.test"
			}
			svc = New(q, cfg)
			svc.worker.sender = provider
			provider.result = deliveryResult{id: "accepted-fake"}
			if _, err := svc.worker.Step(t.Context()); err != nil {
				t.Fatal(err)
			}
			var row outbox
			if err := db.DB.First(&row, initial.ID).Error; err != nil {
				t.Fatal(err)
			}
			if change == "sender" || change == "reply-to" {
				if row.Reason != "configuration_changed" || len(provider.bodies) != 1 {
					t.Fatal("changed newsletter profile retried")
				}
			} else if row.State != "accepted" || len(provider.bodies) != 2 {
				t.Fatal("unchanged newsletter profile failed to retry")
			}
		})
	}
}
