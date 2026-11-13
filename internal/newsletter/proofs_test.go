package newsletter

import (
	"bytes"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
)

func TestErasurePreventsOlderQueuedCapture(t *testing.T) {
	s, now := fixture(t)
	request(t, s, "erase@example.test", "en")
	step(t, s)
	journal, err := s.Store.InitializeLifecycleJournal(t.Context(), s.Store.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	var actor store.NewsletterContactAuthority
	if err := s.Store.DB.Raw("SELECT u.user_id,u.email_key,a.incarnation FROM users u JOIN account_incarnations a ON a.user_id=u.user_id").Scan(&actor).Error; err != nil {
		t.Fatal(err)
	}
	actor.RequestReference = "verified-erasure"
	*now = now.Add(61 * time.Second)
	request(t, s, "erase@example.test", "en")
	if _, err := journal.EraseNewsletterContact(t.Context(), actor, bytes.Repeat([]byte{8}, 32), *now, store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	step(t, s)
	if count(t, s, "users") != 0 {
		t.Fatal("older job recreated erased contact")
	}
	*now = now.Add(time.Hour)
	request(t, s, "erase@example.test", "en")
	step(t, s)
	if count(t, s, "users") != 1 {
		t.Fatal("fresh deliberate request was blocked")
	}
}

func links(t *testing.T, s *Service) (string, string) {
	t.Helper()
	var row mailqueue.Record
	if err := s.Store.DB.Where("key LIKE 'newsletter/%'").Order("id DESC").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	fields, _, err := mailqueue.OpenProtected(s.Keys, row, s.now(), false)
	if err != nil {
		t.Fatal(err)
	}
	confirm, err := url.Parse(fields["confirm"])
	if err != nil {
		t.Fatal(err)
	}
	unsubscribe, err := url.Parse(fields["unsubscribe"])
	if err != nil {
		t.Fatal(err)
	}
	if confirm.Host != "mychef.dev" || confirm.Fragment == "" || unsubscribe.Fragment == "" {
		t.Fatal("invalid proof origins")
	}
	return confirm.Fragment, unsubscribe.Fragment
}

func status(t *testing.T, s *Service) string {
	t.Helper()
	var value string
	if err := s.Store.SQL.QueryRow("SELECT status FROM newsletter_subscriptions").Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestProofConfirmationWithdrawalAndFreshResubscription(t *testing.T) {
	s, now := fixture(t)
	request(t, s, "reader@example.test", "fr-BE")
	step(t, s)
	confirm, unsubscribe := links(t, s)
	if err := s.Confirm(t.Context(), confirm); err != nil || status(t, s) != "confirmed" {
		t.Fatal("confirmation failed", err)
	}
	if err := s.Confirm(t.Context(), confirm); !errors.Is(err, ErrProof) {
		t.Fatal("confirmation replay accepted", err)
	}
	*now = now.Add(61 * time.Second)
	request(t, s, "reader@example.test", "nl-BE") // This job precedes withdrawal.
	if err := s.Unsubscribe(t.Context(), unsubscribe); err != nil || status(t, s) != "unsubscribed" {
		t.Fatal("withdrawal failed", err)
	}
	step(t, s)
	if count(t, s, "email_outbox") != 1 {
		t.Fatal("delayed job issued mail after withdrawal")
	}
	if err := s.Unsubscribe(t.Context(), unsubscribe); err != nil {
		t.Fatal("withdrawal retry failed", err)
	}
	*now = now.Add(time.Hour)
	request(t, s, "reader@example.test", "nl-BE")
	step(t, s)
	if status(t, s) != "unsubscribed" {
		t.Fatal("anonymous resubmission reactivated consent")
	}
	newConfirm, _ := links(t, s)
	if err := s.Confirm(t.Context(), newConfirm); err != nil || status(t, s) != "confirmed" {
		t.Fatal("fresh confirmation failed", err)
	}
	if err := s.Unsubscribe(t.Context(), unsubscribe); err != nil || status(t, s) != "confirmed" {
		t.Fatal("old consumed withdrawal affected fresh consent", err)
	}
	var locale string
	if err := s.Store.SQL.QueryRow("SELECT locale FROM newsletter_subscriptions").Scan(&locale); err != nil || locale != "nl-BE" {
		t.Fatal("proved locale not applied", err)
	}
}

func TestProofPurposeExpiryAndAddressBinding(t *testing.T) {
	for _, scenario := range []string{"wrong-purpose", "expired", "address-changed", "withdraw-first"} {
		t.Run(scenario, func(t *testing.T) {
			s, now := fixture(t)
			request(t, s, "proof@example.test", "en")
			step(t, s)
			confirm, unsubscribe := links(t, s)
			switch scenario {
			case "wrong-purpose":
				if err := s.Confirm(t.Context(), unsubscribe); !errors.Is(err, ErrProof) {
					t.Fatal("wrong purpose accepted")
				}
			case "expired":
				*now = now.Add(24 * time.Hour)
			case "address-changed":
				if err := s.Store.DB.Exec("UPDATE users SET email='changed@example.test',email_key='changed@example.test'").Error; err != nil {
					t.Fatal(err)
				}
			case "withdraw-first":
				if err := s.Unsubscribe(t.Context(), unsubscribe); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "wrong-purpose" {
				if err := s.Confirm(t.Context(), confirm); !errors.Is(err, ErrProof) {
					t.Fatal("invalid proof accepted", err)
				}
			}
		})
	}
}

func TestProofWithdrawalKeepsIdentityAndCancelsQueuedLinks(t *testing.T) {
	s, _ := fixture(t)
	request(t, s, "withdraw@example.test", "en")
	step(t, s)
	_, unsubscribe := links(t, s)
	if err := s.Unsubscribe(t.Context(), unsubscribe); err != nil {
		t.Fatal(err)
	}
	if count(t, s, "users") != 1 {
		t.Fatal("withdrawal deleted identity")
	}
	var row mailqueue.Record
	if err := s.Store.DB.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != "failed" || row.Reason != "proof_invalidated" {
		t.Fatal("obsolete queued mail remains deliverable")
	}
	if _, _, err := mailqueue.OpenProtected(s.Keys, row, s.now(), false); err == nil {
		t.Fatal("withdrawn links remain recoverable")
	}
}
