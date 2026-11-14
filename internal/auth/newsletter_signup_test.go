package auth

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/newsletter"
	"github.com/LowGok/my-chef/internal/store"
)

type contactConsent struct {
	Status, Locale, ConsentVersion, Source string
	RequestedUnix, Revision, ProvedOrder   int64
	ConfirmedUnix, WithdrawnUnix           *int64
}

func readContactConsent(t *testing.T, s *store.Store, id int64) contactConsent {
	t.Helper()
	var state contactConsent
	if err := s.DB.Table("newsletter_subscriptions").Where("user_id=?", id).Take(&state).Error; err != nil {
		t.Fatal(err)
	}
	return state
}

func contactFixture(t *testing.T, signup *SignUp, address, state string) (*newsletter.Service, store.User, string, string) {
	t.Helper()
	n := &newsletter.Service{Store: signup.Store, Keys: signup.Keys, RateKey: signup.RateKey, Origin: "https://mychef.dev", ConsentVersion: "newsletter-v1", Now: signup.Now}
	if wait, err := n.Request(t.Context(), "192.0.2.41", newsletter.Input{Email: address, Locale: "fr-BE", ConsentVersion: n.ConsentVersion}); err != nil || wait != 0 {
		t.Fatalf("capture request: %d %v", wait, err)
	}
	if worked, err := n.Step(t.Context()); !worked || err != nil {
		t.Fatalf("capture step: %v %v", worked, err)
	}
	var user store.User
	if err := signup.Store.DB.Where("email_key=?", strings.ToLower(address)).Take(&user).Error; err != nil {
		t.Fatal(err)
	}
	var row mailqueue.Record
	if err := signup.Store.DB.Where("key LIKE 'newsletter/%' AND recipient=?", address).Order("id DESC").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	fields, _, err := mailqueue.OpenProtected(signup.Keys, row, signup.now(), false)
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
	if state != "pending" {
		if err := n.Confirm(t.Context(), confirmation.Fragment); err != nil {
			t.Fatal(err)
		}
	}
	if state == "unsubscribed" {
		if err := n.Unsubscribe(t.Context(), withdrawal.Fragment); err != nil {
			t.Fatal(err)
		}
	}
	return n, user, confirmation.Fragment, withdrawal.Fragment
}

func requestContactSignup(t *testing.T, signup *SignUp, address string) (string, string) {
	t.Helper()
	anonymous, err := New(signup.Store).Rotate(t.Context(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	details := SignUpDetails{FirstName: "Newsletter", LastName: "Reader", Username: "reader"}
	if wait, err := signup.Request(t.Context(), "192.0.2.42", SignUpRequest{Email: address, SignUpDetails: details}); err != nil || wait != 0 {
		t.Fatalf("signup request: %d %v", wait, err)
	}
	if worked, err := signup.Step(t.Context()); !worked || err != nil {
		t.Fatalf("signup step: %v %v", worked, err)
	}
	return anonymous, signUpLink(t, signup, signup.now())
}

func contactCompletion(proof string) SignUpCompletion {
	return SignUpCompletion{Proof: proof, Password: "newsletter reader app password", SignUpDetails: SignUpDetails{FirstName: "Chosen", LastName: "Profile", Username: "chosen-reader"}}
}

func TestNewsletterSignupPreservesIdentityAndConsent(t *testing.T) {
	for _, state := range []string{"pending", "confirmed", "unsubscribed"} {
		t.Run(state, func(t *testing.T) {
			signup, _ := signUpFixture(t)
			n, user, confirmation, withdrawal := contactFixture(t, signup, "reader@example.test", state)
			before := readContactConsent(t, signup.Store, user.ID)
			anonymous, proof := requestContactSignup(t, signup, "READER@example.test")
			if _, err := New(signup.Store).Login(t.Context(), user.Email, contactCompletion(proof).Password); !errors.Is(err, ErrCredentials) {
				t.Fatal("contact admitted before signup")
			}
			if err := signup.Complete(t.Context(), anonymous, contactCompletion(proof)); err != nil {
				t.Fatal(err)
			}
			account, err := New(signup.Store).Login(t.Context(), user.Email, contactCompletion(proof).Password)
			if err != nil || account.ID != user.ID || account.IsNewsletterOnly || account.EmailVerifiedAt == nil || account.Username != "chosen-reader" {
				t.Fatalf("promotion lost identity/profile: %v", err)
			}
			if after := readContactConsent(t, signup.Store, user.ID); !reflect.DeepEqual(before, after) {
				t.Fatalf("signup changed consent: before=%+v after=%+v", before, after)
			}
			if err := signup.Complete(t.Context(), anonymous, contactCompletion(proof)); !errors.Is(err, ErrSignUpProof) {
				t.Fatalf("completion replay: %v", err)
			}
			if state == "pending" {
				if err := n.Confirm(t.Context(), confirmation); err != nil {
					t.Fatal("pending confirmation lost after promotion", err)
				}
			}
			if state != "unsubscribed" {
				if err := n.Unsubscribe(t.Context(), withdrawal); err != nil {
					t.Fatal("withdrawal lost after promotion", err)
				}
			}
		})
	}
}

func TestNewsletterSignupDeletedContactCannotBeReplacedThroughOldProof(t *testing.T) {
	signup, now := signUpFixture(t)
	_, user, _, _ := contactFixture(t, signup, "reader@example.test", "pending")
	anonymous, proof := requestContactSignup(t, signup, user.Email)
	j, err := signup.Store.InitializeLifecycleJournal(t.Context(), signup.Store.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	var incarnation string
	if err := signup.Store.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=?", user.ID).Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	actor := store.NewsletterContactAuthority{UserID: user.ID, EmailKey: user.EmailKey, Incarnation: incarnation, RequestReference: "fixture-confirmed-erasure"}
	if _, err := j.EraseNewsletterContact(t.Context(), actor, signup.RateKey, *now, store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(61 * time.Second)
	_, replacement, _, _ := contactFixture(t, signup, user.Email, "pending")
	if err := signup.Complete(t.Context(), anonymous, contactCompletion(proof)); !errors.Is(err, ErrSignUpProof) {
		t.Fatalf("old proof promoted replacement: %v", err)
	}
	var kept store.User
	if err := signup.Store.DB.First(&kept, replacement.ID).Error; err != nil || !kept.IsNewsletterOnly {
		t.Fatal("replacement contact changed", err)
	}
}

func TestNewsletterSignupCaptureAfterProofUsesOneIdentity(t *testing.T) {
	signup, _ := signUpFixture(t)
	anonymous, proof := requestContactSignup(t, signup, "reader@example.test")
	_, user, _, _ := contactFixture(t, signup, "reader@example.test", "pending")
	if err := signup.Complete(t.Context(), anonymous, contactCompletion(proof)); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := signup.Store.DB.Model(&store.User{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("duplicate identity", err)
	}
	account, err := New(signup.Store).Login(t.Context(), user.Email, contactCompletion(proof).Password)
	if err != nil || account.ID != user.ID {
		t.Fatal("late capture lost identity", err)
	}
}
