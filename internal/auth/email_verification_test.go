package auth

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
)

func TestEmailVerificationRollsBackWhenReplacementSessionCannotBeWritten(t *testing.T) {
	ctx := context.Background()
	signup, now := signUpFixture(t)
	verification := &EmailVerification{SignUp: signup}
	manager := New(signup.Store)
	user, err := manager.Register(ctx, Registration{Email: "Fixture@Example.Test", Password: "fixture existing password",
		Username: "fixture", FirstName: "Fixture", LastName: "User"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := manager.Rotate(ctx, &user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := verification.Resend(ctx, user.ID, session, "192.0.2.2"); err != nil || wait != 0 {
		t.Fatalf("resend failed: %v", err)
	}
	var message mailqueue.Record
	if err := signup.Store.DB.Where("template = ?", mailqueue.EmailVerificationTemplate).First(&message).Error; err != nil {
		t.Fatal(err)
	}
	fields, _, err := mailqueue.OpenProtected(signup.Keys, message, *now, false)
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(fields["link"])
	if err != nil || link.Fragment == "" {
		t.Fatal("missing proof")
	}
	if err := signup.Store.DB.Exec(`CREATE TRIGGER reject_verification_session BEFORE INSERT ON sessions
BEGIN SELECT RAISE(ABORT, 'injected failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, token, err := verification.Confirm(ctx, user.ID, session, link.Fragment); !errors.Is(err, ErrEmailVerificationUnavailable) || token != "" {
		t.Fatal("failed verification issued a replacement session")
	}
	if err := signup.Store.DB.First(&user, user.ID).Error; err != nil || user.EmailVerifiedAt != nil {
		t.Fatal("failed verification changed the account")
	}
	var proof store.EmailVerificationProof
	if err := signup.Store.DB.Where("user_id = ?", user.ID).Take(&proof).Error; err != nil || proof.Consumed != nil {
		t.Fatal("failed verification consumed the proof")
	}
	if _, err := manager.Load(ctx, session); err != nil {
		t.Fatal("failed verification revoked the original session")
	}
	if err := signup.Store.DB.Exec("DROP TRIGGER reject_verification_session").Error; err != nil {
		t.Fatal(err)
	}
	if _, replacement, err := verification.Confirm(ctx, user.ID, session, link.Fragment); err != nil || replacement == session {
		t.Fatal("a rolled back verification could not be retried")
	}
}
