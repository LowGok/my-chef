package auth

import (
	"reflect"
	"testing"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
)

func TestNewsletterPromotionKeepsAnalyticsChoiceSeparate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[enabled], func(t *testing.T) {
			s, now := signUpFixture(t)
			_, contact, _, _ := contactFixture(t, s, "reader@example.test", "confirmed")
			before := readContactConsent(t, s.Store, contact.ID)
			signupAnalytics(t, s, now)
			details := SignUpDetails{FirstName: "Newsletter", LastName: "Reader", Username: "reader", AnalyticsEnabled: &enabled, AnalyticsNoticeVersion: "core-v1"}
			if _, err := s.Request(t.Context(), "192.0.2.43", SignUpRequest{SignUpDetails: details, Email: contact.Email}); err != nil {
				t.Fatal(err)
			}
			if worked, err := s.Step(t.Context()); !worked || err != nil {
				t.Fatal("signup work", worked, err)
			}
			proof := signUpLink(t, s, *now)
			anonymous, err := New(s.Store).Rotate(t.Context(), nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Complete(t.Context(), anonymous, SignUpCompletion{SignUpDetails: details, Proof: proof, Password: "FixtureSafePassword2026!"}); err != nil {
				t.Fatal(err)
			}
			var user store.User
			if err := s.Store.DB.First(&user, contact.ID).Error; err != nil || user.IsNewsletterOnly {
				t.Fatal("original contact was not promoted", err)
			}
			if after := readContactConsent(t, s.Store, contact.ID); !reflect.DeepEqual(before, after) {
				t.Fatal("analytics enrollment changed newsletter consent")
			}
			var actual bool
			if err := s.Store.SQL.QueryRow(`SELECT p.enabled FROM analytics_preferences p JOIN account_incarnations a ON a.incarnation=p.incarnation WHERE a.user_id=?`, contact.ID).Scan(&actual); err != nil || actual != enabled {
				t.Fatal("signup lost its independent analytics choice", err)
			}
		})
	}
}

func TestNewsletterDestinationRetirementWithAnalyticsFence(t *testing.T) {
	change, now, account, cookie := changeFixture(t, true)
	signupAnalytics(t, change.SignUp, now)
	n, contact, _, _ := contactFixture(t, change.SignUp, "destination@example.test", "confirmed")
	journal, err := change.Store.InitializeLifecycleJournal(t.Context(), change.Store.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	change.Lifecycle, change.Newsletter = journal, n
	if wait, err := change.Start(t.Context(), account.ID, cookie, "192.0.2.45", contact.Email, changeFixturePassword); err != nil || wait != 0 {
		t.Fatal("start", wait, err)
	}
	for _, proof := range []string{deliveredChangeProof(t, change, *now, mailqueue.EmailChangeCurrentTemplate, "approve"), deliveredChangeProof(t, change, *now, mailqueue.EmailChangeProposedTemplate, "link")} {
		if _, err := change.Decide(t.Context(), proof); err != nil {
			t.Fatal(err)
		}
	}
	var remaining int64
	if err := change.Store.DB.Model(&store.User{}).Where("user_id=?", contact.ID).Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatal("placeholder retirement failed", err)
	}
	user, err := New(change.Store).Login(t.Context(), contact.Email, changeFixturePassword)
	if err != nil || user.ID != account.ID || readContactConsent(t, change.Store, account.ID).Status != "confirmed" {
		t.Fatal("account or newsletter consent changed", err)
	}
}
