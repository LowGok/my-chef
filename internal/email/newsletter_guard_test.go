package email

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
)

func TestNewsletterDispatchWaitsForJournalReconciliation(t *testing.T) {
	for _, phase := range []string{"prepared", "marker-commit", "missing-rollback-proof"} {
		t.Run(phase, func(t *testing.T) {
			db, req, now, keys := protectedFixture(t)
			j, err := db.InitializeLifecycleJournal(t.Context(), db.Path+".lifecycle")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = j.Close() }()
			req.Key = "newsletter/uncertain-worker"
			req.Template = mailqueue.NewsletterENTemplate
			req.Fields = map[string]string{"confirm": "https://mychef.dev/newsletter/confirm/#N1ngJYuGtWdZT7czVq0pcc7bcLSeTog-L9VQq9YR73Q", "unsubscribe": "https://mychef.dev/newsletter/unsubscribe/#N1ngJYuGtWdZT7czVq0pcc7bcLSeTog-L9VQq9YR73Q"}
			if phase != "prepared" {
				req.Template = mailqueue.NewsletterWithdrawalFRTemplate
				if phase == "missing-rollback-proof" {
					req.Template = mailqueue.NewsletterWithdrawalNLTemplate
				}
				delete(req.Fields, "confirm")
			}
			id, err := mailqueue.EnqueueProtected(t.Context(), db, nil, now, now.Add(time.Hour), keys, req)
			if err != nil {
				t.Fatal(err)
			}
			operation, incarnation := strings.Repeat("a", 32), strings.Repeat("b", 32)
			if err := j.PrepareDeletion(t.Context(), operation, incarnation); err != nil {
				t.Fatal(err)
			}
			if phase == "marker-commit" {
				if err := db.DB.Exec("INSERT INTO lifecycle_operations(operation,incarnation,created_ns) VALUES(?,?,?)", operation, incarnation, now.UnixNano()).Error; err != nil {
					t.Fatal(err)
				}
			}
			if phase == "missing-rollback-proof" && !errors.Is(j.ReconcileIntents(t.Context()), store.ErrLifecycleUncertain) {
				t.Fatal("missing app receipt incorrectly cleared the prepared deletion")
			}
			if !errors.Is(j.CheckReady(t.Context()), store.ErrLifecycleUncertain) {
				t.Fatal("fixture is not unsettled")
			}
			queue := Queue{Store: db, ProofKeys: keys, Now: func() time.Time { return now }, Lifecycle: j}
			provider := &proofSender{result: deliveryResult{id: "fake-reconciled-newsletter"}}
			worker := Worker{Queue: queue, sender: provider, From: "mychef@example.test", FingerprintKey: bytes.Repeat([]byte{9}, 32)}
			if worked, err := worker.Step(t.Context()); worked || err == nil || len(provider.bodies) != 0 {
				t.Fatalf("unsettled journal dispatched mail: worked=%v err=%v deliveries=%d", worked, err, len(provider.bodies))
			}
			var row outbox
			if err := db.DB.First(&row, id).Error; err != nil || row.Attempts != 0 || row.State != "queued" {
				t.Fatal("pause consumed or claimed the message", err)
			}
			service := New(queue, config.Email{Fake: true})
			if service.Ready(t.Context()) {
				t.Fatal("mail readiness ignored unsettled journal")
			}
			if phase != "marker-commit" {
				if err := db.DB.Exec("INSERT INTO lifecycle_operations(operation,incarnation,created_ns) VALUES(?,?,?)",
					operation, incarnation, now.UnixNano()).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := j.ReconcileIntents(t.Context()); err != nil {
				t.Fatal(err)
			}
			if worked, err := worker.Step(t.Context()); !worked || err != nil || len(provider.bodies) != 1 {
				t.Fatalf("reconciled delivery: %v %v", worked, err)
			}
			if worked, err := worker.Step(t.Context()); worked || err != nil || len(provider.bodies) != 1 {
				t.Fatal("reconciliation duplicated delivery", err)
			}
		})
	}
}
