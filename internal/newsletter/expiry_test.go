package newsletter

import (
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

func TestNewsletterRetentionKeepsConfirmedAndPromotedAccounts(t *testing.T) {
	s, now := fixture(t)
	for _, address := range []string{"pending@example.test", "confirmed@example.test", "promoted@example.test", "withdrawn@example.test"} {
		request(t, s, address, "en")
		step(t, s)
		confirmation, withdrawal := links(t, s)
		if address == "confirmed@example.test" {
			if err := s.Confirm(t.Context(), confirmation); err != nil {
				t.Fatal(err)
			}
		}
		if address == "withdrawn@example.test" {
			if err := s.Unsubscribe(t.Context(), withdrawal); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Store.DB.Model(&store.User{}).Where("email_key=?", "promoted@example.test").Update("isNewsletterOnly", false).Error; err != nil {
		t.Fatal(err)
	}
	journal, err := s.Store.InitializeLifecycleJournal(t.Context(), s.Store.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	*now = now.Add(31 * 24 * time.Hour)
	if err := s.Sweep(t.Context(), journal, 7, 30, store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	var emails []string
	if err := s.Store.DB.Model(&store.User{}).Order("email_key").Pluck("email_key", &emails).Error; err != nil {
		t.Fatal(err)
	}
	if len(emails) != 2 || emails[0] != "confirmed@example.test" || emails[1] != "promoted@example.test" {
		t.Fatal(emails)
	}
	if count(t, s, "newsletter_requests") != 0 {
		t.Fatal("expired request retained")
	}
}
