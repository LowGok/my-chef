package email

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/newsletter"
)

func newsletterOnlyProfile() config.Email {
	return config.Email{Fake: true, NewsletterFrom: "MyChef <newsletter@mychef.dev>", NewsletterReplyTo: "feedback@gkldevelopment.com"}
}

func TestNewsletterOnlyRefusesUnrelatedQueueWork(t *testing.T) {
	q, now := testOutbox(t)
	enqueueTestMail(t, q, "wrong-purpose")
	svc := NewNewsletter(q, newsletterOnlyProfile())
	provider := &proofSender{result: deliveryResult{id: "must-not-send"}}
	svc.worker.sender = provider
	if svc.Webhook() != nil || svc.FixtureInbox() != nil {
		t.Fatal("role exposed mail control surfaces")
	}
	svc.Run(t.Context()) // Must stop on the unexpected row, not wait for cancellation.
	var row outbox
	if err := q.Store.DB.Where("key=?", "wrong-purpose").Take(&row).Error; err != nil {
		t.Fatal(err, now)
	}
	if svc.Ready(t.Context()) || row.State != "failed" || row.Reason != "newsletter_role_template" || row.Attempts != 0 || len(provider.bodies) != 0 {
		t.Fatal("unrelated app mail dispatched or readiness remained true", row.State, row.Reason)
	}
}

func TestNewsletterOnlySixTemplatesAndStableRetry(t *testing.T) {
	for _, locale := range []string{"en", "fr-BE", "nl-BE"} {
		for _, transfer := range []bool{false, true} {
			t.Run(locale+map[bool]string{false: "/confirm", true: "/transfer"}[transfer], func(t *testing.T) {
				db, initial, now, keys := newsletterFixture(t, locale, transfer)
				svc := NewNewsletter(Queue{Store: db, ProofKeys: keys, Now: func() time.Time { return now }}, newsletterOnlyProfile())
				provider := &proofSender{result: deliveryResult{temporary: true}}
				svc.worker.sender = provider
				if worked, err := svc.worker.Step(t.Context()); err != nil || !worked || len(provider.bodies) != 1 {
					t.Fatal("first attempt", err)
				}
				now = now.Add(time.Minute)
				provider.result = deliveryResult{id: "fake-newsletter-role"}
				if worked, err := svc.worker.Step(t.Context()); err != nil || !worked || len(provider.bodies) != 2 {
					t.Fatal("retry", err)
				}
				if !bytes.Equal(provider.bodies[0], provider.bodies[1]) {
					t.Fatal("retry changed payload")
				}
				var payload sendPayload
				if json.Unmarshal(provider.bodies[1], &payload) != nil || payload.From != "MyChef <newsletter@mychef.dev>" || payload.ReplyTo != "feedback@gkldevelopment.com" {
					t.Fatal("wrong role sender")
				}
				var row outbox
				if err := db.DB.First(&row, initial.ID).Error; err != nil || row.State != "accepted" {
					t.Fatal("not accepted", err)
				}
				if _, _, err := mailqueue.OpenProtected(keys, row, now, false); err == nil {
					t.Fatal("finished link retained")
				}
			})
		}
	}
}

func TestNewsletterOnlyStaleAndExpiredNeverReachProvider(t *testing.T) {
	for _, change := range []string{"revision", "expired"} {
		t.Run(change, func(t *testing.T) {
			db, initial, now, keys := newsletterFixture(t, "en", false)
			if change == "revision" {
				if err := db.DB.Exec("UPDATE newsletter_subscriptions SET revision=revision+1").Error; err != nil {
					t.Fatal(err)
				}
			} else {
				now = now.Add(25 * time.Hour)
			}
			svc := NewNewsletter(Queue{Store: db, ProofKeys: keys, Now: func() time.Time { return now }}, newsletterOnlyProfile())
			provider := &proofSender{result: deliveryResult{id: "must-not-send"}}
			svc.worker.sender = provider
			if worked, err := svc.worker.Step(t.Context()); err != nil || !worked || len(provider.bodies) != 0 {
				t.Fatal("stale authority sent", err)
			}
			var row outbox
			if err := db.DB.First(&row, initial.ID).Error; err != nil || row.State != "failed" {
				t.Fatal("stale row left eligible", err)
			}
		})
	}
}

func TestNewsletterOnlyWithdrawalWaitsForProvider(t *testing.T) {
	db, row, now, keys := newsletterFixture(t, "en", false)
	fields, _, err := mailqueue.OpenProtected(keys, row, now, false)
	if err != nil {
		t.Fatal(err)
	}
	_, proof, _ := strings.Cut(fields["unsubscribe"], "#")
	n := &newsletter.Service{Store: db, Keys: keys, RateKey: bytes.Repeat([]byte{8}, 32), Origin: "https://mychef.dev", ConsentVersion: "newsletter-v1", Now: func() time.Time { return now }}
	svc := NewNewsletter(Queue{Store: db, ProofKeys: keys, Now: func() time.Time { return now }}, newsletterOnlyProfile())
	provider := blockedSender{started: make(chan struct{}), release: make(chan struct{})}
	svc.worker.sender = provider
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	sent := make(chan error, 1)
	go func() { _, err := svc.worker.Step(ctx); sent <- err }()
	select {
	case <-provider.started:
	case <-ctx.Done():
		t.Fatal("no dispatch")
	}
	withdrew := make(chan error, 1)
	go func() { withdrew <- n.Unsubscribe(ctx, proof) }()
	select {
	case err := <-withdrew:
		close(provider.release)
		t.Fatal("withdrawal did not wait", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(provider.release)
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if err := <-withdrew; err != nil {
		t.Fatal(err)
	}
	if worked, err := svc.worker.Step(ctx); err != nil || worked {
		t.Fatal("withdrawal allowed another dispatch", err)
	}
}
