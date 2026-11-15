package newsletter

import (
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/config"
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

func TestNewsletterRetentionPoliciesAreIndependent(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		pending, withdrawn         config.NewsletterRetention
		keepPending, keepWithdrawn bool
	}{
		{"none/none", config.NewsletterRetention{NoExpiry: true}, config.NewsletterRetention{NoExpiry: true}, true, true},
		{"none/finite", config.NewsletterRetention{NoExpiry: true}, config.NewsletterRetention{Days: 30}, true, false},
		{"finite/none", config.NewsletterRetention{Days: 7}, config.NewsletterRetention{NoExpiry: true}, false, true},
		{"finite/finite", config.NewsletterRetention{Days: 7}, config.NewsletterRetention{Days: 30}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, now := fixture(t)
			for _, address := range []string{"aged-pending@example.test", "aged-withdrawn@example.test"} {
				request(t, s, address, "en")
				step(t, s)
				_, withdrawal := links(t, s)
				if address == "aged-withdrawn@example.test" {
					if err := s.Unsubscribe(t.Context(), withdrawal); err != nil {
						t.Fatal(err)
					}
				}
			}
			var before []store.User
			if err := s.Store.DB.Order("email_key").Find(&before).Error; err != nil {
				t.Fatal(err)
			}
			request(t, s, "expired-queue@example.test", "en")
			j, err := s.Store.InitializeLifecycleJournal(t.Context(), s.Store.Path+".lifecycle")
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			*now = now.Add(90 * 24 * time.Hour)
			for range 2 {
				if err := s.Sweep(t.Context(), j, tc.pending, tc.withdrawn, store.AccountDeletionOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			for i, keep := range []bool{tc.keepPending, tc.keepWithdrawn} {
				var total int64
				if err := s.Store.DB.Model(&store.User{}).Where("user_id=? AND email_key=? AND isNewsletterOnly=1", before[i].ID, before[i].EmailKey).Count(&total).Error; err != nil {
					t.Fatal(err)
				}
				if (total == 1) != keep {
					t.Fatalf("retained identity %d=%d; wanted %t", before[i].ID, total, keep)
				}
			}
			if count(t, s, "newsletter_requests") != 0 {
				t.Fatal("expired queued requests retained")
			}
			var active int64
			if err := s.Store.DB.Table("newsletter_proofs").Where("purpose='confirm' AND (consumed_unix IS NULL OR proposal<>'')").Count(&active).Error; err != nil {
				t.Fatal(err)
			}
			if active != 0 {
				t.Fatal("expired confirmation authority or sealed proposal retained")
			}
		})
	}
}

func TestNoExpiryDoesNotExtendConfirmationOrExpireWithdrawal(t *testing.T) {
	s, now := fixture(t)
	request(t, s, "retained@example.test", "en")
	step(t, s)
	confirmation, withdrawal := links(t, s)
	j, err := s.Store.InitializeLifecycleJournal(t.Context(), s.Store.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	*now = now.Add(90 * 24 * time.Hour)
	if err := s.Sweep(t.Context(), j, config.NewsletterRetention{NoExpiry: true}, config.NewsletterRetention{NoExpiry: true}, store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Confirm(t.Context(), confirmation); err == nil {
		t.Fatal("expired confirmation accepted")
	}
	if err := s.Unsubscribe(t.Context(), withdrawal); err != nil {
		t.Fatal("current withdrawal authority expired", err)
	}
	if count(t, s, "users") != 1 {
		t.Fatal("withdrawal deleted retained contact")
	}
}
