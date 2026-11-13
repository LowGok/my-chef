package email

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
)

func TestNewsletterUsesProtectedRetryAndExpiry(t *testing.T) {
	for _, template := range []string{mailqueue.NewsletterENTemplate, mailqueue.NewsletterFRTemplate, mailqueue.NewsletterNLTemplate} {
		t.Run(template, func(t *testing.T) {
			db, req, now, keys := protectedFixture(t)
			req.Key = "newsletter/retry"
			req.Template = template
			req.Fields = map[string]string{"confirm": "https://mychef.dev/newsletter/confirm/#N1ngJYuGtWdZT7czVq0pcc7bcLSeTog-L9VQq9YR73Q", "unsubscribe": "https://mychef.dev/newsletter/unsubscribe/#N1ngJYuGtWdZT7czVq0pcc7bcLSeTog-L9VQq9YR73Q"}
			id, err := mailqueue.EnqueueProtected(t.Context(), db, nil, now, now.Add(24*time.Hour), keys, req)
			if err != nil {
				t.Fatal(err)
			}
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
