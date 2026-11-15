package newsletter

import (
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

// Bootstrap only the current synthetic newsletter-only rows. This fixture does
// not claim migration, restore or physical-cleanup readiness.
func minimisationJournal(t *testing.T, s *Service) *store.LifecycleJournal {
	t.Helper()
	j, err := s.Store.InitializeLifecycleJournal(t.Context(), s.Store.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	key, err := store.DeriveNewsletterIdentityKey(s.RateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.InitializeNewsletterIdentity(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	var users []store.User
	if err := s.Store.DB.Where("isNewsletterOnly=1").Find(&users).Error; err != nil {
		t.Fatal(err)
	}
	for i, user := range users {
		var sub subscription
		if err := s.Store.DB.Take(&sub, "user_id=?", user.ID).Error; err != nil {
			t.Fatal(err)
		}
		var incarnation string
		if err := s.Store.SQL.QueryRowContext(t.Context(), "SELECT incarnation FROM account_incarnations WHERE user_id=?", user.ID).Scan(&incarnation); err != nil {
			t.Fatal(err)
		}
		tag, err := key.Tag(user.Email)
		if err != nil {
			t.Fatal(err)
		}
		date := store.Ptr(sub.RequestedUnix)
		switch sub.Status {
		case "confirmed":
			date = sub.ConfirmedUnix
		case "unsubscribed":
			date = sub.WithdrawnUnix
		}
		record := store.NewsletterIdentityRecord{UserID: user.ID, Incarnation: incarnation, Class: "contact", KeyVersion: key.Version(), AddressTag: tag, Status: sub.Status, Revision: sub.Revision, DecisionOrder: sub.ProvedOrder, NoticeVersion: store.Ptr(sub.ConsentVersion), DecisionUnix: date}
		op := strings.Repeat(string(rune('a'+i)), 32)
		intent, err := j.PrepareNewsletterIdentity(t.Context(), op, record)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Store.DB.Transaction(func(tx *gorm.DB) error { return j.MarkNewsletterIdentityCommit(t.Context(), tx, intent) }); err != nil {
			t.Fatal(err)
		}
		if err := j.CommitNewsletterIdentity(t.Context(), op); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.CompleteNewsletterIdentityBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.Lifecycle = j
	return j
}

func requireReducedContact(t *testing.T, s *Service, id int64, wantStatus string) {
	t.Helper()
	var total int
	if err := s.Store.SQL.QueryRowContext(t.Context(), `SELECT count(*) FROM users u JOIN newsletter_subscriptions n ON n.user_id=u.user_id JOIN newsletter_identity_matches m ON m.user_id=u.user_id JOIN account_incarnations a ON a.user_id=u.user_id AND a.incarnation=m.incarnation WHERE u.user_id=? AND u.isNewsletterOnly=1 AND u.newsletter_minimised=1 AND u.email IS NULL AND u.email_key IS NULL AND u.username IS NULL AND n.email_key IS NULL AND n.locale IS NULL AND n.source IS NULL AND n.requested_unix IS NULL AND n.confirmed_unix IS NULL AND n.status=?`, id, wantStatus).Scan(&total); err != nil || total != 1 {
		t.Fatal("same-ID reduced contact absent", err)
	}
	var pending bool
	if err := s.Store.SQL.QueryRowContext(t.Context(), "SELECT physical_cleanup_pending FROM newsletter_identity_namespace").Scan(&pending); err != nil || !pending {
		t.Fatal("logical cleanup incorrectly certified active-file removal", err)
	}
}

func TestMinimisationExpiredPendingKeepsIDAndOldWithdrawal(t *testing.T) {
	s, now := fixture(t)
	request(t, s, "inactive@example.test", "en")
	step(t, s)
	confirmation, withdrawal := links(t, s)
	var user store.User
	if err := s.Store.DB.First(&user).Error; err != nil {
		t.Fatal(err)
	}
	j := minimisationJournal(t, s)
	*now = now.Add(25 * time.Hour)
	if err := s.Minimise(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	requireReducedContact(t, s, user.ID, "pending")
	if count(t, s, "email_outbox") != 0 {
		t.Fatal("obsolete mail recipient/body retained")
	}
	if err := s.Confirm(t.Context(), confirmation); err == nil {
		t.Fatal("expired confirmation survived")
	}
	// Pending physical cleanup must not block a valid withdrawal-only action.
	if err := s.Unsubscribe(t.Context(), withdrawal); err != nil {
		t.Fatal("original withdrawal no longer works", err)
	}
	requireReducedContact(t, s, user.ID, "unsubscribed")
	record, found, err := j.NewsletterIdentity(t.Context(), user.ID)
	if err != nil || !found || record.Status != "unsubscribed" || !record.Minimised || record.DecisionOrder == 0 {
		t.Fatal("withdrawal not journaled", err)
	}
}

func TestMinimisationRemovesObsoleteProofMailAndProviderReferences(t *testing.T) {
	s, now := fixture(t)
	request(t, s, "withdrawn@example.test", "fr-BE")
	step(t, s)
	_, withdrawal := links(t, s)
	if err := s.Unsubscribe(t.Context(), withdrawal); err != nil {
		t.Fatal(err)
	}
	var user store.User
	if err := s.Store.DB.First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Store.DB.Exec("UPDATE email_outbox SET provider_id='synthetic-provider',fingerprint='obsolete-address-body-canary'").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Store.DB.Exec("INSERT INTO email_events VALUES('event','synthetic-provider','delivered',1,1)").Error; err != nil {
		t.Fatal(err)
	}
	j := minimisationJournal(t, s)
	*now = now.Add(25 * time.Hour)
	for range 2 {
		if err := s.Minimise(t.Context(), j); err != nil {
			t.Fatal(err)
		}
	}
	requireReducedContact(t, s, user.ID, "unsubscribed")
	for _, table := range []string{"newsletter_proofs", "email_outbox", "email_events", "account_mail_owners"} {
		if count(t, s, table) != 0 {
			t.Fatal("obsolete purpose-owned references retained", table)
		}
	}
}

func TestMinimisationDefersCurrentAuthorityAndPreservesConfirmedAccount(t *testing.T) {
	s, now := fixture(t)
	request(t, s, "live@example.test", "en")
	step(t, s)
	var live store.User
	if err := s.Store.DB.First(&live).Error; err != nil {
		t.Fatal(err)
	}
	request(t, s, "confirmed@example.test", "nl-BE")
	step(t, s)
	confirmation, _ := links(t, s)
	if err := s.Confirm(t.Context(), confirmation); err != nil {
		t.Fatal(err)
	}
	app := store.User{Email: "app@example.test", Username: "app", Password: "kept", FirstName: "Kept"}
	if err := s.Store.DB.Create(&app).Error; err != nil {
		t.Fatal(err)
	}
	j := minimisationJournal(t, s)
	if err := s.Minimise(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	var u store.User
	if err := s.Store.DB.First(&u, live.ID).Error; err != nil || u.Email != live.Email || u.NewsletterMinimised {
		t.Fatal("live confirmation address removed", err)
	}
	*now = now.Add(25 * time.Hour)
	if err := s.Minimise(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	requireReducedContact(t, s, live.ID, "pending")
	var kept int
	if err := s.Store.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM users WHERE (email='confirmed@example.test' OR email='app@example.test') AND newsletter_minimised=0").Scan(&kept); err != nil || kept != 2 {
		t.Fatal("confirmed subscriber/app account changed", err)
	}
}

func TestMinimisationKeepsLiveSignupAndVerificationAuthority(t *testing.T) {
	for _, kind := range []string{"signup", "verification", "email-change", "recovery"} {
		t.Run(kind, func(t *testing.T) {
			s, now := fixture(t)
			request(t, s, "authority@example.test", "en")
			step(t, s)
			*now = now.Add(25 * time.Hour)
			var user store.User
			if err := s.Store.DB.First(&user).Error; err != nil {
				t.Fatal(err)
			}
			expires := now.Add(time.Hour).Unix()
			switch kind {
			case "signup":
				tag, err := store.EmailRateBucket(s.RateKey, "signup-proof/address", user.EmailKey)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Store.DB.Exec("INSERT INTO signup_proofs VALUES('proof','hash',?,?,NULL,'sealed')", tag, expires).Error; err != nil {
					t.Fatal(err)
				}
			case "verification":
				if err := s.Store.DB.Exec("INSERT INTO email_verification_proofs VALUES('proof',?,'hash','tag',?,NULL)", user.ID, expires).Error; err != nil {
					t.Fatal(err)
				}
			case "email-change":
				if err := s.Store.DB.Exec("INSERT INTO pending_email_changes(id,user_id,original_email,original_key,proposed_email,proposed_key,expires) VALUES('change',?,'authority@example.test','authority@example.test','next@example.test','next@example.test',?)", user.ID, expires).Error; err != nil {
					t.Fatal(err)
				}
			case "recovery":
				// Existing recovery schema requires a verified account. A contact-bound
				// delivery job independently keeps its current bounded recipient authority.
				_, err := mailqueue.EnqueueProtected(t.Context(), s.Store, nil, *now, now.Add(time.Hour), s.Keys, mailqueue.Request{OwnerID: user.ID, Key: "authority-job", Recipient: user.Email, Template: mailqueue.EmailVerificationTemplate, Fields: map[string]string{"link": "https://mychef.dev/app/verify-email#synthetic"}})
				if err != nil {
					t.Fatal(err)
				}
			}
			j := minimisationJournal(t, s)
			if err := s.Minimise(t.Context(), j); err != nil {
				t.Fatal(err)
			}
			var kept store.User
			if err := s.Store.DB.First(&kept, user.ID).Error; err != nil || kept.Email != user.Email || kept.NewsletterMinimised {
				t.Fatal("current purpose authority removed", err)
			}
		})
	}
}

func TestMinimisationRefusesAmbiguousContactProfile(t *testing.T) {
	s, now := fixture(t)
	request(t, s, "ambiguous@example.test", "en")
	step(t, s)
	j := minimisationJournal(t, s)
	if err := s.Store.DB.Exec("UPDATE users SET firstname='Unexpected profile'").Error; err != nil {
		t.Fatal(err)
	}
	*now = now.Add(25 * time.Hour)
	if err := s.Minimise(t.Context(), j); err == nil {
		t.Fatal("ambiguous profile silently erased")
	}
	var u store.User
	if err := s.Store.DB.First(&u).Error; err != nil || u.FirstName != "Unexpected profile" || u.Email == "" {
		t.Fatal("ambiguous account modified", err)
	}
}
