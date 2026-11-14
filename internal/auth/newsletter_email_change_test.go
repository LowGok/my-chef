package auth

import (
	"errors"
	"net/url"
	"testing"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/newsletter"
	"github.com/LowGok/my-chef/internal/store"
)

func TestNewsletterDestinationEmailChangePreservesAccountAndConsent(t *testing.T) {
	for _, state := range []string{"pending", "confirmed", "unsubscribed"} {
		for _, reverse := range []bool{false, true} {
			name := state + "/current-first"
			if reverse {
				name = state + "/destination-first"
			}
			t.Run(name, func(t *testing.T) {
				change, now, account, cookie := changeFixture(t, true)
				n, contact, oldConfirmation, oldWithdrawal := contactFixture(t, change.SignUp, "destination@example.test", state)
				before := readContactConsent(t, change.Store, contact.ID)
				journal, err := change.Store.InitializeLifecycleJournal(t.Context(), change.Store.Path+".lifecycle")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = journal.Close() }()
				change.Lifecycle, change.Newsletter = journal, n
				if wait, err := change.Start(t.Context(), account.ID, cookie, "192.0.2.43", contact.Email, changeFixturePassword); err != nil || wait != 0 {
					t.Fatalf("start: %d %v", wait, err)
				}
				first := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeCurrentTemplate, "approve")
				second := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeProposedTemplate, "link")
				if reverse {
					first, second = second, first
				}
				if result, err := change.Decide(t.Context(), first); err != nil || result.Status != "pending" {
					t.Fatalf("first proof: %+v %v", result, err)
				}
				if result, err := change.Decide(t.Context(), second); err != nil || result.Status != "changed" || result.UserID != account.ID {
					t.Fatalf("verified destination was rejected: %+v %v", result, err)
				}
				updated, err := New(change.Store).Login(t.Context(), contact.Email, changeFixturePassword)
				if err != nil || updated.ID != account.ID || updated.Username != account.Username || updated.IsNewsletterOnly {
					t.Fatalf("lost app account: %v", err)
				}
				var remaining int64
				if err := change.Store.DB.Model(&store.User{}).Where("user_id=?", contact.ID).Count(&remaining).Error; err != nil || remaining != 0 {
					t.Fatal("placeholder retained", err)
				}
				after := readContactConsent(t, change.Store, account.ID)
				if after.Status != before.Status || after.Locale != before.Locale || after.ConsentVersion != before.ConsentVersion || after.Source != before.Source || after.RequestedUnix != before.RequestedUnix || after.Revision <= before.Revision {
					t.Fatalf("consent transfer: before=%+v after=%+v", before, after)
				}
				if err := n.Confirm(t.Context(), oldConfirmation); !errors.Is(err, newsletter.ErrProof) {
					t.Fatalf("obsolete contact confirmation: %v", err)
				}
				if err := n.Unsubscribe(t.Context(), oldWithdrawal); !errors.Is(err, newsletter.ErrProof) {
					t.Fatalf("obsolete contact withdrawal: %v", err)
				}
				if _, err := New(change.Store).Load(t.Context(), cookie); !errors.Is(err, ErrSession) {
					t.Fatal("email change retained session")
				}
				if state == "confirmed" {
					var row mailqueue.Record
					if err := change.Store.DB.Where("key LIKE 'newsletter/%' AND recipient=?", contact.Email).Order("id DESC").Take(&row).Error; err != nil {
						t.Fatal("fresh withdrawal mail missing", err)
					}
					fields, _, err := mailqueue.OpenProtected(change.Keys, row, *now, false)
					if err != nil {
						t.Fatal(err)
					}
					link, err := url.Parse(fields["unsubscribe"])
					if err != nil || link.Fragment == "" {
						t.Fatal("fresh withdrawal absent", err)
					}
					if err := n.Unsubscribe(t.Context(), link.Fragment); err != nil {
						t.Fatal("fresh withdrawal unusable", err)
					}
				}
			})
		}
	}
}
