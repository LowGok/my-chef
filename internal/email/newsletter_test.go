package email

import (
	"bytes"
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/newsletter"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func TestNewsletterUsesProtectedRetryAndExpiry(t *testing.T) {
	for _, template := range []string{mailqueue.NewsletterENTemplate, mailqueue.NewsletterFRTemplate, mailqueue.NewsletterNLTemplate} {
		t.Run(template, func(t *testing.T) {
			locale := map[string]string{mailqueue.NewsletterENTemplate: "en", mailqueue.NewsletterFRTemplate: "fr-BE", mailqueue.NewsletterNLTemplate: "nl-BE"}[template]
			db, initial, now, keys := newsletterFixture(t, locale, false)
			id := initial.ID

			provider := &proofSender{result: deliveryResult{temporary: true}}
			worker := Worker{Queue: Queue{Store: db, ProofKeys: keys, Now: func() time.Time { return now }}, sender: provider, From: "mychef@example.test", FingerprintKey: bytes.Repeat([]byte{9}, 32), Random: func() float64 { return .5 }}
			if _, err := worker.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			provider.result = deliveryResult{id: "fake-newsletter"}
			if _, err := worker.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(provider.bodies) != 2 || !bytes.Equal(provider.bodies[0], provider.bodies[1]) {
				t.Fatal("retry changed protected content")
			}
			var row outbox
			if err := db.DB.First(&row, id).Error; err != nil || row.State != "accepted" {
				t.Fatal("fake delivery did not finish", err)
			}
			if _, _, err := mailqueue.OpenProtected(keys, row, now, false); err == nil {
				t.Fatal("accepted proof links were retained")
			}
		})
	}
}

// newsletterFixture uses the real capture and transfer services so dispatch tests
// carry live SQL identity, consent, incarnation and proof authority.
func newsletterFixture(t *testing.T, locale string, transfer bool) (*store.Store, mailqueue.Record, time.Time, *mailqueue.ProofKeys) {
	t.Helper()
	db, _, now, keys := protectedFixture(t)
	n := &newsletter.Service{Store: db, Keys: keys, RateKey: bytes.Repeat([]byte{8}, 32), Origin: "https://mychef.dev", ConsentVersion: "newsletter-v1", Now: func() time.Time { return now }}
	if _, err := n.Request(t.Context(), "192.0.2.10", newsletter.Input{Email: "recipient@example.test", Locale: locale, ConsentVersion: "newsletter-v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	var row mailqueue.Record
	if err := db.DB.Where("key LIKE 'newsletter/%'").Order("id DESC").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if transfer {
		fields, _, err := mailqueue.OpenProtected(keys, row, now, false)
		if err != nil {
			t.Fatal(err)
		}
		confirm, err := url.Parse(fields["confirm"])
		if err != nil {
			t.Fatal(err)
		}
		if err := n.Confirm(t.Context(), confirm.Fragment); err != nil {
			t.Fatal(err)
		}
		if err := db.DB.Transaction(func(tx *gorm.DB) error {
			var user store.User
			if err := tx.First(&user).Error; err != nil {
				return err
			}
			consent, err := newsletter.ReadContactConsent(tx, user.ID)
			if err != nil {
				return err
			}
			return n.TransferContactConsent(t.Context(), tx, now, consent, user)
		}); err != nil {
			t.Fatal(err)
		}
		row = mailqueue.Record{}
		if err := db.DB.Where("key LIKE 'newsletter/%'").Order("id DESC").Take(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, row, now, keys
}
