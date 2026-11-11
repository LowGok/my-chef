package auth

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
)

const changeFixturePassword = "fixture address change password"

func changeFixture(t *testing.T, verified bool) (*EmailChange, *time.Time, store.User, string) {
	t.Helper()
	ctx := context.Background()
	signup, now := signUpFixture(t)
	manager := New(signup.Store)
	user, err := manager.Register(ctx, Registration{Email: "Original@Example.Test", Password: changeFixturePassword,
		Username: "original", FirstName: "Fixture", LastName: "User"})
	if err != nil {
		t.Fatal(err)
	}
	if verified {
		value := now.Unix()
		if err := signup.Store.DB.Model(&user).Update("email_verified_at", value).Error; err != nil {
			t.Fatal(err)
		}
		user.EmailVerifiedAt = &value
	}
	token, err := manager.Rotate(ctx, &user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	return &EmailChange{SignUp: signup}, now, user, token
}

func deliveredChangeProof(t *testing.T, service *EmailChange, now time.Time, template, field string) string {
	t.Helper()
	var row mailqueue.Record
	if err := service.Store.DB.Where("template = ?", template).Order("id DESC").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	fields, _, err := mailqueue.OpenProtected(service.Keys, row, now, false)
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(fields[field])
	if err != nil || link.Fragment == "" {
		t.Fatal("missing protected proof")
	}
	return link.Fragment
}

func TestVerifiedEmailChangeRequiresBothInboxesAndFencesOldLogin(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "current first"
		if reverse {
			name = "new first"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			change, now, user, token := changeFixture(t, true)
			manager := New(change.Store)
			oldLogin, err := manager.verifyLogin(ctx, user.Email, changeFixturePassword)
			if err != nil {
				t.Fatal(err)
			}
			const proposed = "Proposed@Example.Test"
			if wait, err := change.Start(ctx, user.ID, token, "192.0.2.1", proposed, changeFixturePassword); err != nil || wait != 0 {
				t.Fatalf("could not start address change: %d %v", wait, err)
			}
			current := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeCurrentTemplate, "approve")
			newProof := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeProposedTemplate, "link")
			view, err := change.Inspect(ctx, current)
			if err != nil || view.Action != "approve" || view.MaskedAddress == "" || strings.Contains(view.MaskedAddress, "Proposed") {
				t.Fatal("old inbox did not receive only the masked proposed address")
			}
			if reverse {
				current, newProof = newProof, current
			}
			if result, err := change.Decide(ctx, current); err != nil || result.Status != "pending" {
				t.Fatalf("first proof did not leave the change pending: %v", err)
			}
			if _, err := manager.Login(ctx, user.Email, changeFixturePassword); err != nil {
				t.Fatal("first confirmation disabled old address")
			}
			if _, err := manager.Login(ctx, proposed, changeFixturePassword); !errors.Is(err, ErrCredentials) {
				t.Fatal("first confirmation activated new address")
			}
			result, err := change.Decide(ctx, newProof)
			if err != nil || result.Status != "changed" || result.UserID != user.ID {
				t.Fatalf("second inbox could not finish change: %v", err)
			}
			if _, err := manager.Load(ctx, token); !errors.Is(err, ErrSession) {
				t.Fatal("a prior session survived the email change")
			}
			if _, _, err := manager.completeLogin(ctx, oldLogin, "", false); !errors.Is(err, ErrCredentials) {
				t.Fatal("a login verified before the change accepted the old address")
			}
			updated, err := manager.Login(ctx, "proposed@example.test", changeFixturePassword)
			if err != nil || updated.Email != proposed || updated.EmailVerifiedAt == nil {
				t.Fatal("new address did not become the verified delivery address")
			}
			newSession, err := manager.Rotate(ctx, &user.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			if wait, err := change.Start(ctx, user.ID, newSession, "192.0.2.1", "too-soon@example.test", changeFixturePassword); err != nil || wait != 86400 {
				t.Fatalf("completed change did not enforce the daily cooldown: %d %v", wait, err)
			}
			if _, err := change.Decide(ctx, newProof); !errors.Is(err, ErrEmailProof) {
				t.Fatal("completed proof could be replayed")
			}
			var notices []mailqueue.Record
			if err := change.Store.DB.Where("template = ?", mailqueue.EmailChangedTemplate).Order("id").Find(&notices).Error; err != nil ||
				len(notices) != 2 || notices[0].Recipient != user.Email || notices[1].Recipient != proposed {
				t.Fatal("completion notices did not reach the correct two inboxes")
			}
		})
	}
}

func TestAddressChangeCanBeCancelledAfterApprovalAndThenSuperseded(t *testing.T) {
	ctx := context.Background()
	change, now, user, token := changeFixture(t, true)
	if _, err := change.Start(ctx, user.ID, token, "192.0.2.1", "proposed@example.test", changeFixturePassword); err != nil {
		t.Fatal(err)
	}
	approve := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeCurrentTemplate, "approve")
	cancel := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeCurrentTemplate, "cancel")
	proposed := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeProposedTemplate, "link")
	if result, err := change.Decide(ctx, approve); err != nil || result.Status != "pending" {
		t.Fatal("old inbox could not approve")
	}
	if result, err := change.Decide(ctx, cancel); err != nil || result.Status != "cancelled" {
		t.Fatal("cancellation link failed after approval")
	}
	if _, err := change.Decide(ctx, proposed); !errors.Is(err, ErrEmailProof) {
		t.Fatal("cancelled change could still be confirmed")
	}
	if _, err := change.Start(ctx, user.ID, token, "192.0.2.1", "try-again@example.test", changeFixturePassword); err != nil {
		t.Fatal(err)
	}
	if wait, err := change.Start(ctx, user.ID, token, "192.0.2.1", "too-many@example.test", changeFixturePassword); err != nil || wait != 86400 {
		t.Fatalf("cancelled start did not count toward the daily start limit: %d %v", wait, err)
	}
}

func TestAddressCorrectionCannotBypassVerificationWhenOriginalInboxWins(t *testing.T) {
	for _, originalFirst := range []bool{true, false} {
		name := "correction first"
		if originalFirst {
			name = "original first"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			change, now, user, token := changeFixture(t, false)
			verify := &EmailVerification{SignUp: change.SignUp}
			if _, err := verify.Resend(ctx, user.ID, token, "192.0.2.1"); err != nil {
				t.Fatal(err)
			}
			oldProof := deliveredChangeProof(t, change, *now, mailqueue.EmailVerificationTemplate, "link")
			const corrected = "corrected@example.test"
			if _, err := change.Start(ctx, user.ID, token, "192.0.2.1", corrected, "wrong current password"); !errors.Is(err, ErrCurrentPassword) {
				t.Fatal("correction accepted an incorrect current password")
			}
			if _, err := change.Start(ctx, user.ID, token, "192.0.2.1", corrected, changeFixturePassword); err != nil {
				t.Fatal(err)
			}
			newProof := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeProposedTemplate, "link")
			var count int64
			if err := change.Store.DB.Model(&mailqueue.Record{}).Where("template = ?", mailqueue.EmailChangeCurrentTemplate).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("correction sent an approval to an unverified address")
			}
			if originalFirst {
				if _, _, err := verify.Confirm(ctx, user.ID, token, oldProof); err != nil {
					t.Fatal(err)
				}
				if _, err := change.Decide(ctx, newProof); !errors.Is(err, ErrEmailProof) {
					t.Fatal("correction remained valid after original address verification")
				}
			} else {
				if result, err := change.Decide(ctx, newProof); err != nil || result.Status != "changed" {
					t.Fatal("new mailbox alone did not verify a correction")
				}
				if _, _, err := verify.Confirm(ctx, user.ID, token, oldProof); !errors.Is(err, ErrSession) {
					t.Fatal("superseded address could still be verified")
				}
				var notices []mailqueue.Record
				if err := change.Store.DB.Where("template = ?", mailqueue.EmailChangedTemplate).Find(&notices).Error; err != nil ||
					len(notices) != 1 || notices[0].Recipient != corrected {
					t.Fatal("correction did not notify only the newly verified address")
				}
			}
		})
	}
}

func TestEmailChangeRollsBackOnNoticeFailureAndPasswordChangeInvalidatesPendingProofs(t *testing.T) {
	ctx := context.Background()
	change, now, user, token := changeFixture(t, true)
	if _, err := change.Start(ctx, user.ID, token, "192.0.2.1", "proposed@example.test", changeFixturePassword); err != nil {
		t.Fatal(err)
	}
	approve := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeCurrentTemplate, "approve")
	proposed := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeProposedTemplate, "link")
	if _, err := change.Decide(ctx, approve); err != nil {
		t.Fatal(err)
	}
	if err := change.Store.DB.Exec(`CREATE TRIGGER reject_email_change_notice BEFORE INSERT ON email_outbox
WHEN NEW.template='email-changed/v1'
BEGIN SELECT RAISE(ABORT, 'injected failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := change.Decide(ctx, proposed); !errors.Is(err, ErrEmailChangeUnavailable) {
		t.Fatal("expected injected outbox failure")
	}
	manager := New(change.Store)
	if got, err := manager.Login(ctx, user.Email, changeFixturePassword); err != nil || got.Email != user.Email {
		t.Fatal("failed finalization changed the sign-in address")
	}
	if _, err := manager.Load(ctx, token); err != nil {
		t.Fatal("failed finalization revoked a session")
	}
	if _, err := change.Inspect(ctx, proposed); err != nil {
		t.Fatal("failed finalization consumed the pending proof")
	}
	if _, _, err := manager.ChangePassword(ctx, user.ID, token, changeFixturePassword, "another password fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := change.Decide(ctx, proposed); !errors.Is(err, ErrEmailProof) {
		t.Fatal("old approval survived a password change")
	}
	if _, err := change.commitStart(ctx, user, token, "held@example.test", "held@example.test"); !errors.Is(err, ErrSession) {
		t.Fatal("a start held before password change could issue approvals")
	}
}

func TestEmailChangeCancellationAndFinalizationHaveOneWinner(t *testing.T) {
	ctx := context.Background()
	change, now, user, token := changeFixture(t, true)
	if _, err := change.Start(ctx, user.ID, token, "192.0.2.1", "proposed@example.test", changeFixturePassword); err != nil {
		t.Fatal(err)
	}
	approve := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeCurrentTemplate, "approve")
	cancel := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeCurrentTemplate, "cancel")
	proposed := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeProposedTemplate, "link")
	if _, err := change.Decide(ctx, approve); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	outcomes := make(chan error, 2)
	for _, proof := range []string{cancel, proposed} {
		group.Go(func() {
			_, err := change.Decide(ctx, proof)
			outcomes <- err
		})
	}
	group.Wait()
	close(outcomes)
	successes := 0
	for err := range outcomes {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrEmailProof) {
			t.Fatalf("unexpected failure in finalization race: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one transition, got %d", successes)
	}
}

func TestAddressCollisionDoesNotChangeTheAccountOrRevealOwnershipAtStart(t *testing.T) {
	ctx := context.Background()
	change, now, user, token := changeFixture(t, true)
	const occupied = "claimed@example.test"
	manager := New(change.Store)
	if _, err := manager.Register(ctx, Registration{Email: occupied, Password: changeFixturePassword,
		Username: "claimed", FirstName: "Other", LastName: "User"}); err != nil {
		t.Fatal(err)
	}
	if wait, err := change.Start(ctx, user.ID, token, "192.0.2.1", occupied, changeFixturePassword); err != nil || wait != 0 {
		t.Fatal("start exposed an occupied target address")
	}
	approve := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeCurrentTemplate, "approve")
	proposed := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeProposedTemplate, "link")
	if _, err := change.Decide(ctx, approve); err != nil {
		t.Fatal(err)
	}
	if _, err := change.Decide(ctx, proposed); !errors.Is(err, ErrEmailProof) {
		t.Fatal("occupied address did not fail safely")
	}
	if actual, err := manager.Login(ctx, user.Email, changeFixturePassword); err != nil || actual.Email != user.Email {
		t.Fatal("collision changed the original account")
	}
	if _, err := manager.Load(ctx, token); err != nil {
		t.Fatal("collision revoked the original session")
	}
	if _, err := change.Inspect(ctx, proposed); !errors.Is(err, ErrEmailProof) {
		t.Fatal("a rejected change could later be replayed")
	}
}

func TestChangeStartReturnsLongestActiveRetry(t *testing.T) {
	ctx := context.Background()
	change, now, user, token := changeFixture(t, true)
	for _, recipient := range []string{"first@example.test", "second@example.test"} {
		if wait, err := change.Start(ctx, user.ID, token, "192.0.2.10", recipient, changeFixturePassword); wait != 0 || err != nil {
			t.Fatalf("could not prepare accepted starts: wait=%d err=%v", wait, err)
		}
	}
	const limitedSource = "192.0.2.12"
	bucket, err := store.EmailRateBucket(change.RateKey, "email-change-start/source", limitedSource)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		result, err := change.Store.ReserveEmailLimits(ctx, nil, *now, store.EmailLimit{
			Bucket: bucket, Maximum: 20, Window: 30 * time.Minute,
		})
		if err != nil || result.RetryAfter != 0 {
			t.Fatal("could not prepare network attempt limit")
		}
	}
	wait, err := change.Start(ctx, user.ID, token, limitedSource, "third@example.test", changeFixturePassword)
	if err != nil || wait != 86400 {
		t.Fatalf("overlapping limits: want 86400 seconds, got %d (%v)", wait, err)
	}
}
