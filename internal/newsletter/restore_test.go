package newsletter_test

import (
	"bytes"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/newsletter"
	"github.com/LowGok/my-chef/internal/store"
)

func restoreCapture(t *testing.T, n *newsletter.Service, address string) (string, string) {
	t.Helper()
	if wait, err := n.Request(t.Context(), "192.0.2.50", newsletter.Input{Email: address, Locale: "en", ConsentVersion: n.ConsentVersion}); err != nil || wait != 0 {
		t.Fatalf("capture: %d %v", wait, err)
	}
	if worked, err := n.Step(t.Context()); !worked || err != nil {
		t.Fatalf("worker: %v %v", worked, err)
	}
	var row mailqueue.Record
	if err := n.Store.DB.Where("key LIKE 'newsletter/%' AND recipient=?", address).Order("id DESC").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	fields, _, err := mailqueue.OpenProtected(n.Keys, row, n.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	confirmation, err := url.Parse(fields["confirm"])
	if err != nil {
		t.Fatal(err)
	}
	withdrawal, err := url.Parse(fields["unsubscribe"])
	if err != nil {
		t.Fatal(err)
	}
	return confirmation.Fragment, withdrawal.Fragment
}

func TestRestoreNewsletterResetFailureDoesNotPublish(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "app.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	j, err := s.InitializeLifecycleJournal(t.Context(), s.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	keys, err := mailqueue.NewProofKeys("test", map[string][]byte{"test": bytes.Repeat([]byte{9}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	n := &newsletter.Service{Store: s, Keys: keys, RateKey: bytes.Repeat([]byte{8}, 32), Origin: "https://mychef.dev", ConsentVersion: "newsletter-v1", Now: func() time.Time { return now }, Lifecycle: j}
	proof, _ := restoreCapture(t, n, "contact@example.test")
	if err := n.Confirm(t.Context(), proof); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("CREATE TRIGGER fail_restore_consent BEFORE UPDATE ON newsletter_subscriptions WHEN NEW.status='pending' BEGIN SELECT RAISE(ABORT,'injected'); END").Error; err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "refused.db")
	if err := store.Restore(t.Context(), backup, destination, store.AccountRestoreOptions{JournalPath: j.Path}); err == nil {
		t.Fatal("injected reset failure accepted")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("partially reset destination published")
	}
	after, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source backup changed", err)
	}
}

func TestRestoreNewsletterRequiresFreshConfirmationAndKeepsAppAccount(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "app.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	j, err := s.InitializeLifecycleJournal(t.Context(), s.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	keys, err := mailqueue.NewProofKeys("test", map[string][]byte{"test": bytes.Repeat([]byte{9}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	n := &newsletter.Service{Store: s, Keys: keys, RateKey: bytes.Repeat([]byte{8}, 32), Origin: "https://mychef.dev", ConsentVersion: "newsletter-v1", Now: func() time.Time { return now }, Lifecycle: j}
	account, err := auth.New(s).Register(t.Context(), auth.Registration{Email: "account@example.test", Username: "restored-account", FirstName: "Restore", LastName: "Fixture", Password: "restore fixture account password"})
	if err != nil {
		t.Fatal(err)
	}
	appProof, _ := restoreCapture(t, n, account.Email)
	if err := n.Confirm(t.Context(), appProof); err != nil {
		t.Fatal(err)
	}
	contactProof, contactWithdrawal := restoreCapture(t, n, "contact@example.test")
	if err := n.Confirm(t.Context(), contactProof); err != nil {
		t.Fatal(err)
	}
	pendingProof, _ := restoreCapture(t, n, "pending@example.test")
	if _, err := n.Request(t.Context(), "192.0.2.51", newsletter.Input{Email: "queued@example.test", Locale: "en", ConsentVersion: n.ConsentVersion}); err != nil {
		t.Fatal(err)
	}
	if _, err := mailqueue.Enqueue(t.Context(), s, nil, now, mailqueue.Request{Key: "ordinary-restore-control", Recipient: account.Email, Template: "provider-smoke-test/v1", Fields: map[string]string{"testID": "restore-fixture", "timestamp": now.Format(time.RFC3339)}}); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "before-withdrawal.db")
	if err := s.Backup(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	if err := n.Unsubscribe(t.Context(), contactWithdrawal); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		destination := filepath.Join(t.TempDir(), "restored.db")
		if err := store.Restore(t.Context(), backup, destination, store.AccountRestoreOptions{JournalPath: j.Path}); err != nil {
			t.Fatal(err)
		}
		restored, err := store.Open(destination, false)
		if err != nil {
			t.Fatal(err)
		}
		paired, err := restored.OpenLifecycleJournal(t.Context(), j.Path)
		if err != nil {
			t.Fatal(err)
		}
		n.Store, n.Lifecycle = restored, paired
		var active int
		if err := restored.SQL.QueryRow("SELECT count(*) FROM newsletter_subscriptions WHERE status!='pending'").Scan(&active); err != nil || active != 0 {
			t.Fatalf("restore revived consent: %d %v", active, err)
		}
		for _, proof := range []string{appProof, contactProof, pendingProof} {
			if err := n.Confirm(t.Context(), proof); !errors.Is(err, newsletter.ErrProof) {
				t.Fatalf("old confirmation survived: %v", err)
			}
		}
		if err := n.Unsubscribe(t.Context(), contactWithdrawal); err != nil && !errors.Is(err, newsletter.ErrProof) {
			t.Fatal(err)
		}
		if worked, err := n.Step(t.Context()); worked || err != nil {
			t.Fatalf("restored request replayed: %v %v", worked, err)
		}
		var mail int
		if err := restored.SQL.QueryRow("SELECT count(*) FROM email_outbox WHERE key LIKE 'newsletter/%' AND (state IN ('queued','sending') OR json_extract(data,'$.links') IS NOT NULL)").Scan(&mail); err != nil || mail != 0 {
			t.Fatalf("restored newsletter mail retained: %d %v", mail, err)
		}
		if err := restored.SQL.QueryRow("SELECT count(*) FROM email_outbox WHERE key='ordinary-restore-control'").Scan(&mail); err != nil || mail != 1 {
			t.Fatal("ordinary app email changed", err)
		}
		kept, err := auth.New(restored).Login(t.Context(), account.Email, "restore fixture account password")
		if err != nil || kept.ID != account.ID || kept.IsNewsletterOnly {
			t.Fatal("app account changed", err)
		}
		now = now.Add(2 * time.Minute)
		fresh, _ := restoreCapture(t, n, "contact@example.test")
		if err := n.Confirm(t.Context(), fresh); err != nil {
			t.Fatal("fresh confirmation failed", err)
		}
		if err := restored.SQL.QueryRow("SELECT count(*) FROM newsletter_subscriptions WHERE status='confirmed'").Scan(&active); err != nil || active != 1 {
			t.Fatal("fresh consent not established", err)
		}
		paired.Close()
		restored.Close()
	}
}
