package auth

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/newsletter"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func reducedContactFixture(t *testing.T, state string) (*SignUp, *newsletter.Service, *time.Time, store.User, *store.LifecycleJournal, string) {
	t.Helper()
	signup, now := signUpFixture(t)
	*now = now.Add(-25 * time.Hour)
	n, user, _, withdrawal := contactFixture(t, signup, "reduced@example.test", state)
	j, err := signup.Store.InitializeLifecycleJournal(t.Context(), signup.Store.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	key, err := store.DeriveNewsletterIdentityKey(signup.RateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.InitializeNewsletterIdentity(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	var incarnation string
	if err := signup.Store.SQL.QueryRowContext(t.Context(), "SELECT incarnation FROM account_incarnations WHERE user_id=?", user.ID).Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	before := readContactConsent(t, signup.Store, user.ID)
	date := store.Ptr(before.RequestedUnix)
	switch before.Status {
	case "confirmed":
		date = before.ConfirmedUnix
	case "unsubscribed":
		date = before.WithdrawnUnix
	}
	tag, err := key.Tag(user.Email)
	if err != nil {
		t.Fatal(err)
	}
	record := store.NewsletterIdentityRecord{UserID: user.ID, Incarnation: incarnation, Class: "contact", KeyVersion: key.Version(), AddressTag: tag, Status: before.Status, Revision: before.Revision, DecisionOrder: before.ProvedOrder, NoticeVersion: store.Ptr(before.ConsentVersion), DecisionUnix: date}
	intent, err := j.PrepareNewsletterIdentity(t.Context(), strings.Repeat("a", 32), record)
	if err != nil {
		t.Fatal(err)
	}
	if err := signup.Store.DB.Transaction(func(tx *gorm.DB) error { return j.MarkNewsletterIdentityCommit(t.Context(), tx, intent) }); err != nil {
		t.Fatal(err)
	}
	if err := j.CommitNewsletterIdentity(t.Context(), intent.Operation); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteNewsletterIdentityBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	n.Lifecycle, signup.Lifecycle = j, j
	*now = now.Add(25 * time.Hour)
	if err := n.Minimise(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	if err := j.CleanupNewsletterFiles(t.Context()); err != nil {
		t.Fatal(err)
	}
	return signup, n, now, user, j, withdrawal
}

func TestNewsletterMinimisationSignupPreservesIDChoiceAndWithdrawal(t *testing.T) {
	for _, state := range []string{"pending", "unsubscribed", "confirmed"} {
		t.Run(state, func(t *testing.T) {
			signup, n, _, contact, j, withdrawal := reducedContactFixture(t, state)
			before := readContactConsent(t, signup.Store, contact.ID)
			anonymous, proof := requestContactSignup(t, signup, "REDUCED@example.test")
			var linked int
			if err := signup.Store.SQL.QueryRowContext(t.Context(), `SELECT count(*) FROM signup_proofs p JOIN account_signup_owners o ON o.proof_id=p.id JOIN account_incarnations a ON a.incarnation=o.incarnation JOIN email_outbox e ON e.key='signup/'||p.id JOIN account_mail_owners m ON m.outbox_id=e.id AND m.incarnation=a.incarnation WHERE a.user_id=?`, contact.ID).Scan(&linked); err != nil || linked != 1 {
				t.Fatal("contact-bound signup work lacks incarnation ownership", err)
			}
			if err := signup.Complete(t.Context(), anonymous, contactCompletion(proof)); err != nil {
				t.Fatal("same-ID reduced signup failed", err)
			}
			account, err := New(signup.Store).Login(t.Context(), contact.Email, contactCompletion(proof).Password)
			if err != nil || account.ID != contact.ID || account.IsNewsletterOnly || account.NewsletterMinimised || account.NewsletterOwnershipEpoch == nil {
				t.Fatal("promotion lost identity or restore epoch", err)
			}
			after := readContactConsent(t, signup.Store, contact.ID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("promotion changed newsletter choice/evidence")
			}
			record, found, err := j.NewsletterIdentity(t.Context(), contact.ID)
			if err != nil || !found || record.Class != "app" || record.AddressTag != "" || record.OwnershipEpoch != *account.NewsletterOwnershipEpoch {
				t.Fatal("promotion retained an active old-address match", err)
			}
			if state == "pending" || state == "confirmed" {
				if err := n.Unsubscribe(t.Context(), withdrawal); err != nil {
					t.Fatal("unchanged-mailbox original withdrawal lost", err)
				}
			}
		})
	}
}

func TestNewsletterMinimisationPromotionChangeReuseDeleteSequence(t *testing.T) {
	signup, n, now, contact, j, _ := reducedContactFixture(t, "pending")
	anonymous, proof := requestContactSignup(t, signup, contact.Email)
	if err := signup.Complete(t.Context(), anonymous, contactCompletion(proof)); err != nil {
		t.Fatal(err)
	}
	manager := New(signup.Store)
	account, err := manager.Login(t.Context(), contact.Email, contactCompletion(proof).Password)
	if err != nil {
		t.Fatal(err)
	}
	originalEpoch := *account.NewsletterOwnershipEpoch
	cookie, err := manager.Rotate(t.Context(), &account.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	signup.Newsletter = n
	change := &EmailChange{SignUp: signup}
	if _, err := change.Start(t.Context(), account.ID, cookie, "192.0.2.60", "next@example.test", contactCompletion(proof).Password); err != nil {
		t.Fatal(err)
	}
	first := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeCurrentTemplate, "approve")
	second := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeProposedTemplate, "link")
	if _, err := change.Decide(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if result, err := change.Decide(t.Context(), second); err != nil || result.Status != "changed" {
		t.Fatal("email change failed", err)
	}
	changed, err := manager.Login(t.Context(), "next@example.test", contactCompletion(proof).Password)
	if err != nil || changed.ID != contact.ID || changed.NewsletterOwnershipEpoch == nil || *changed.NewsletterOwnershipEpoch == originalEpoch {
		t.Fatal("email ownership epoch did not advance", err)
	}
	record, found, err := j.NewsletterIdentity(t.Context(), changed.ID)
	if err != nil || !found || record.OwnershipEpoch != *changed.NewsletterOwnershipEpoch || record.AddressTag != "" {
		t.Fatal("changed address journal authority stale", err)
	}
	*now = now.Add(time.Hour)
	if _, err := n.Request(t.Context(), "192.0.2.61", newsletter.Input{Email: contact.Email, Locale: "en", ConsentVersion: n.ConsentVersion}); err != nil {
		t.Fatal(err)
	}
	if worked, err := n.Step(t.Context()); err != nil || !worked {
		t.Fatal("released address capture failed", err)
	}
	fresh, found, err := signup.Store.ResolveEmailOwner(t.Context(), contact.Email)
	if err != nil || !found || fresh.ID <= contact.ID || !fresh.IsNewsletterOnly {
		t.Fatal("released A still belongs to changed app", err)
	}
	freshState, found, err := j.NewsletterIdentity(t.Context(), fresh.ID)
	if err != nil || !found || freshState.Class != "contact" || freshState.Status != "pending" {
		t.Fatal("fresh A inherited another owner/consent", err)
	}
	session, err := manager.Rotate(t.Context(), &changed.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.DeleteAccount(t.Context(), store.AccountActor{UserID: changed.ID, SessionHash: Hash(session), PasswordHash: changed.Password}, strings.Repeat("d", 32), "DELETE", *now, store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := j.NewsletterIdentity(t.Context(), changed.ID); err != nil || found {
		t.Fatal("deleted app restore fence retained active ownership", err)
	}
	*now = now.Add(time.Hour)
	if _, err := n.Request(t.Context(), "192.0.2.62", newsletter.Input{Email: "next@example.test", Locale: "en", ConsentVersion: n.ConsentVersion}); err != nil {
		t.Fatal(err)
	}
	if worked, err := n.Step(t.Context()); err != nil || !worked {
		t.Fatal(err)
	}
	replacement, found, err := signup.Store.ResolveEmailOwner(t.Context(), "next@example.test")
	if err != nil || !found || replacement.ID <= fresh.ID || replacement.ID == changed.ID || !replacement.IsNewsletterOnly {
		t.Fatal("deleted B ID reused", err)
	}
}

func TestNewsletterMinimisationCreationRespectsReservedHighWater(t *testing.T) {
	signup, n, now, _, j, _ := reducedContactFixture(t, "pending")
	const floor int64 = 9007199254740993
	reserved, err := j.ReserveNewsletterUserID(t.Context(), floor)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	if _, err := n.Request(t.Context(), "192.0.2.63", newsletter.Input{Email: "new@example.test", Locale: "en", ConsentVersion: n.ConsentVersion}); err != nil {
		t.Fatal(err)
	}
	if worked, err := n.Step(t.Context()); err != nil || !worked {
		t.Fatal(err)
	}
	user, found, err := signup.Store.ResolveEmailOwner(t.Context(), "new@example.test")
	if err != nil || !found || user.ID <= reserved {
		t.Fatal("capture reused an allocated identity range", err)
	}
	if record, found, err := j.NewsletterIdentity(t.Context(), user.ID); err != nil || !found || record.Incarnation == "" {
		t.Fatal("new contact was not journaled with its incarnation", err)
	}
}
