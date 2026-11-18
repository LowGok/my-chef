package auth

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/store"
)

func usernamePolicyRegister(t *testing.T, m *Manager, address, username string) store.User {
	t.Helper()
	u, err := m.Register(t.Context(), Registration{
		Email: address, Username: username, Password: "synthetic pantry account password",
		FirstName: "Synthetic", LastName: "Cook",
	})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func usernamePolicyProfile(user store.User, username string) ProfileUpdate {
	return ProfileUpdate{Username: username, FirstName: user.FirstName, LastName: user.LastName, Preferences: domain.EmptyPreferences()}
}

func TestSignInIdentifierRegistrationRejectsFoldedName(t *testing.T) {
	m := New(openPasswordTestStore(t))
	usernamePolicyRegister(t, m, "existing@example.test", "Étage")
	_, err := m.Register(t.Context(), Registration{
		Email: "new@example.test", Username: "étage", Password: "a different synthetic pantry password",
		FirstName: "New", LastName: "Cook",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected a case-folded registration conflict, got %v", err)
	}
	var created int64
	if err := m.Store.DB.Model(&store.User{}).Where("email_key=?", "new@example.test").Count(&created).Error; err != nil || created != 0 {
		t.Fatal("a rejected registration changed accounts")
	}
}

func TestSignInIdentifierVerifiedCollisionKeepsProofAndNewsletterConsent(t *testing.T) {
	for _, promotion := range []bool{false, true} {
		name := "direct"
		if promotion {
			name = "newsletter-promotion"
		}
		t.Run(name, func(t *testing.T) {
			signup, _ := signUpFixture(t)
			m := New(signup.Store)
			usernamePolicyRegister(t, m, "alias-owner@example.test", "Étage")
			const target = "fixture-verified@example.test"
			var old store.User
			var consent contactConsent
			if promotion {
				_, old, _, _ = contactFixture(t, signup, target, "confirmed")
				consent = readContactConsent(t, signup.Store, old.ID)
			}
			anonymous, proof := requestContactSignup(t, signup, target)
			attempt := contactCompletion(proof)
			attempt.Username = "étage"
			revoked, err := m.Rotate(t.Context(), nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = m.Revoke(t.Context(), revoked); err != nil {
				t.Fatal(err)
			}
			for _, candidate := range []string{"étage", "free-unclaimed"} {
				bad := attempt
				bad.Username = candidate
				if err := signup.Complete(t.Context(), revoked, bad); !errors.Is(err, ErrSession) {
					t.Fatalf("stale signup authority received an availability-dependent result: %v", err)
				}
			}
			if err = signup.Complete(t.Context(), anonymous, attempt); !errors.Is(err, ErrConflict) {
				t.Fatalf("expected a case-folded signup conflict, got %v", err)
			}
			if _, err = signup.Inspect(t.Context(), proof); err != nil {
				t.Fatal("a naming conflict consumed the proof", err)
			}
			if s, err := m.Load(t.Context(), anonymous); err != nil || s.UserID != nil {
				t.Fatal("a naming conflict changed the anonymous session")
			}
			var appRows int64
			if err := m.Store.DB.Model(&store.User{}).Where("email_key=? AND isNewsletterOnly=0", target).Count(&appRows).Error; err != nil || appRows != 0 {
				t.Fatal("a naming conflict created or promoted an account")
			}
			if promotion && !reflect.DeepEqual(readContactConsent(t, signup.Store, old.ID), consent) {
				t.Fatal("a naming conflict changed newsletter consent")
			}
			attempt.Username = "free-unclaimed"
			if err = signup.Complete(t.Context(), anonymous, attempt); err != nil {
				t.Fatal("the same proof could not be retried with a free username", err)
			}
			got, err := m.Login(t.Context(), target, attempt.Password)
			if err != nil || got.Username != attempt.Username || got.EmailVerifiedAt == nil || (promotion && got.ID != old.ID) {
				t.Fatal("retry failed to bind the verified account", err)
			}
		})
	}
}

func TestSignInIdentifierConcurrentRegistrationAndRenameShareOneKey(t *testing.T) {
	m := New(openPasswordTestStore(t))
	actor := usernamePolicyRegister(t, m, "profile-owner@example.test", "Current")
	start := make(chan struct{})
	result := make(chan error, 2)
	go func() {
		<-start
		_, err := m.Register(t.Context(), Registration{
			Username: "Straße", Email: "registration@example.test", Password: "a synthetic cook registration password",
			FirstName: "Synthetic", LastName: "Cook",
		})
		result <- err
	}()
	go func() {
		<-start
		_, err := m.Update(t.Context(), actor.ID, usernamePolicyProfile(actor, "strasse"))
		result <- err
	}()
	close(start)
	var succeeded, conflicted int
	for range 2 {
		err := <-result
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrConflict) {
			conflicted++
		} else {
			t.Fatalf("registration or rename got an unexpected error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("case-folded alias had %d successful claims and %d conflicts; want one of each", succeeded, conflicted)
	}
}

func TestSignInIdentifierStaleNativeAuthorityNeverReturnsAvailability(t *testing.T) {
	m := New(openPasswordTestStore(t))
	usernamePolicyRegister(t, m, "alias-owner@example.test", "Étage")
	actor := usernamePolicyRegister(t, m, "profile-owner@example.test", "Current")
	for _, expired := range []bool{false, true} {
		anchor, err := m.Rotate(t.Context(), &actor.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		tokens, err := m.NativeBind(t.Context(), anchor, "ios", "")
		if err != nil {
			t.Fatal(err)
		}
		if expired {
			if err = m.Store.DB.Model(&store.Session{}).Where("hash=?", Hash(tokens.AccessToken)).Update("expires_unix", time.Now().Unix()-1).Error; err != nil {
				t.Fatal(err)
			}
		} else if err = m.Revoke(t.Context(), tokens.AccessToken); err != nil {
			t.Fatal(err)
		}
		for _, candidate := range []string{"étage", "free-unclaimed"} {
			if _, err = m.Update(t.Context(), actor.ID, usernamePolicyProfile(actor, candidate), tokens.AccessToken); !errors.Is(err, ErrSession) {
				t.Fatalf("stale native authority received an availability-dependent result: %v", err)
			}
		}
	}
	var got store.User
	if err := m.Store.DB.First(&got, actor.ID).Error; err != nil || got.Username != actor.Username {
		t.Fatal("stale native authority changed the stored username", err)
	}
}

func TestSignInIdentifierGrandfatheredAndNewsletterSpellings(t *testing.T) {
	m := New(openPasswordTestStore(t))
	first := usernamePolicyRegister(t, m, "first@example.test", "Étage")
	second := store.User{Email: "second@example.test", Username: "étage", FirstName: "Legacy", LastName: "Cook", Password: first.Password}
	if err := m.Store.DB.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(t.Context(), first.ID, usernamePolicyProfile(first, first.Username)); err != nil {
		t.Fatal("unchanged grandfathered profile should save", err)
	}
	if _, err := m.Update(t.Context(), first.ID, usernamePolicyProfile(first, "ÉTAGE")); !errors.Is(err, ErrConflict) {
		t.Fatal("a legacy collision allowed a new spelling", err)
	}
	if _, err := m.Update(t.Context(), second.ID, usernamePolicyProfile(second, "Different")); err != nil {
		t.Fatal("legacy collision could not be resolved with a free name", err)
	}
	if _, err := m.Update(t.Context(), first.ID, usernamePolicyProfile(first, "ÉTAGE")); err != nil {
		t.Fatal("sole owner could not edit only the case", err)
	}
	contact := store.User{Email: "reader@example.test", Username: "Newsletter", IsNewsletterOnly: true}
	if err := m.Store.DB.Create(&contact).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(t.Context(), first.ID, usernamePolicyProfile(first, "Newsletter")); !errors.Is(err, ErrConflict) {
		t.Fatal("existing global exact-spelling constraint was weakened", err)
	}
	if _, err := m.Update(t.Context(), first.ID, usernamePolicyProfile(first, "newsletter")); err != nil {
		t.Fatal("newsletter-only record reserved a folded app key", err)
	}
}
