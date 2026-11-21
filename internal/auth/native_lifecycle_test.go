package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
)

func TestNativeAccountActionBindsPasswordAndLiveAccess(t *testing.T) {
	m, user, cookie, _ := passwordChangeFixture(t)
	tokens, err := m.NativeBind(t.Context(), cookie, "ios", "")
	if err != nil {
		t.Fatal(err)
	}
	actor, err := m.VerifyAccountAction(t.Context(), user.ID, tokens.AccessToken, "original test phrase")
	if err != nil || actor.UserID != user.ID || actor.SessionHash != Hash(tokens.AccessToken) || actor.PasswordHash != user.Password {
		t.Fatal("native account action did not bind its live actor")
	}
	if _, err := m.VerifyAccountAction(t.Context(), user.ID, tokens.RefreshToken, "original test phrase"); !errors.Is(err, ErrSession) {
		t.Fatal("refresh authority passed account step-up")
	}
	if _, err := m.NativeRefresh(t.Context(), tokens.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := m.VerifyAccountAction(t.Context(), user.ID, tokens.AccessToken, "original test phrase"); !errors.Is(err, ErrSession) {
		t.Fatal("rotated native access passed account step-up")
	}
}

func TestNativePasswordChangeRevokesEveryPreviousInstallation(t *testing.T) {
	m, user, first, second := passwordChangeFixture(t)
	one, err := m.NativeBind(t.Context(), first, "ios", "")
	if err != nil {
		t.Fatal(err)
	}
	two, err := m.NativeBind(t.Context(), second, "android", "")
	if err != nil {
		t.Fatal(err)
	}
	_, replacement, err := m.ChangePassword(t.Context(), user.ID, one.AccessToken, "original test phrase", "replacement native phrase")
	if err != nil {
		t.Fatal(err)
	}
	for _, previous := range []NativeTokens{one, two} {
		if _, err := m.NativeLoad(t.Context(), previous.AccessToken); !errors.Is(err, ErrSession) {
			t.Fatal("old access survived password change")
		}
		if _, err := m.NativeRefresh(t.Context(), previous.RefreshToken); !errors.Is(err, ErrSession) {
			t.Fatal("old refresh survived password change")
		}
	}
	next, err := m.NativeBind(t.Context(), replacement, "ios", "")
	if err != nil {
		t.Fatal("could not adopt replacement current session")
	}
	if _, err := m.NativeLoad(t.Context(), next.AccessToken); err != nil {
		t.Fatal(err)
	}
}

func TestNativeVerifiedEmailChangeRevokesTheOldFamily(t *testing.T) {
	change, now, user, cookie := changeFixture(t, true)
	m := New(change.Store)
	tokens, err := m.NativeBind(t.Context(), cookie, "ios", "")
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := change.Start(t.Context(), user.ID, tokens.AccessToken, "192.0.2.81", "native-proposed@example.test", changeFixturePassword); err != nil || wait != 0 {
		t.Fatal("native email-change start failed")
	}
	old := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeCurrentTemplate, "approve")
	proposed := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeProposedTemplate, "link")
	if _, err := change.Decide(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if result, err := change.Decide(t.Context(), proposed); err != nil || result.Status != "changed" {
		t.Fatal("native email change did not finish")
	}
	if _, err := m.NativeRefresh(t.Context(), tokens.RefreshToken); !errors.Is(err, ErrSession) {
		t.Fatal("old native family survived email change")
	}
}

func TestNativeRecoveryRevokesSignedInFamiliesAndUsesAnonymousAccess(t *testing.T) {
	recovery, now, user := recoveryFixture(t)
	m := New(recovery.Store)
	cookie := recoverySession(t, m, &user.ID)
	signed, err := m.NativeBind(t.Context(), cookie, "android", "")
	if err != nil {
		t.Fatal(err)
	}
	anonymous, err := m.NativeStart(t.Context(), "ios")
	if err != nil {
		t.Fatal(err)
	}
	proof := issueRecoveryFixture(t, recovery, *now, user.Email)
	if err := recovery.Complete(t.Context(), anonymous.AccessToken, proof, recoveredPassword); err != nil {
		t.Fatal("native anonymous reset failed")
	}
	if _, err := m.NativeRefresh(t.Context(), signed.RefreshToken); !errors.Is(err, ErrSession) {
		t.Fatal("signed-in native family survived recovery")
	}
}

func TestNativeEmailVerificationRotatesItsRestrictedFamily(t *testing.T) {
	change, now, user, cookie := changeFixture(t, false)
	// NativeBind validates the replacement session against the system clock.
	*now = time.Now().UTC()
	m := New(change.Store)
	tokens, err := m.NativeBind(t.Context(), cookie, "ios", "")
	if err != nil {
		t.Fatal(err)
	}
	verification := &EmailVerification{SignUp: change.SignUp}
	if wait, err := verification.Resend(t.Context(), user.ID, tokens.AccessToken, "192.0.2.82"); err != nil || wait != 0 {
		t.Fatal("native verification resend failed")
	}
	proof := deliveredChangeProof(t, change, *now, mailqueue.EmailVerificationTemplate, "link")
	verified, replacement, err := verification.Confirm(t.Context(), user.ID, tokens.AccessToken, proof)
	if err != nil || verified.EmailVerifiedAt == nil {
		t.Fatal("native email verification failed")
	}
	if _, err := m.NativeRefresh(t.Context(), tokens.RefreshToken); !errors.Is(err, ErrSession) {
		t.Fatal("restricted family survived verification")
	}
	next, err := m.NativeBind(t.Context(), replacement, "ios", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.NativeLoad(t.Context(), next.AccessToken); err != nil {
		t.Fatal("verified native authority could not be loaded")
	}
}
