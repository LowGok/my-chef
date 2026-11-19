package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/LowGok/my-chef/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func TestSignInIdentifierUnicodeAliasReachesSameAccount(t *testing.T) {
	m := New(openPasswordTestStore(t))
	u := usernamePolicyRegister(t, m, "fixture-unicode@example.test", "Étage")
	for _, identifier := range []string{"\u2003étage\t", "ÉTAGE", "FIXTURE-Unicode@Example.Test"} {
		anonymous, err := m.Rotate(t.Context(), nil, "")
		if err != nil {
			t.Fatal(err)
		}
		got, tok, err := m.LoginSession(t.Context(), identifier, "synthetic pantry account password", anonymous)
		if err != nil || got.ID != u.ID || tok == "" || tok == anonymous {
			t.Fatalf("unique identifier did not rotate into the original account: %v", err)
		}
		if _, err = m.Load(t.Context(), anonymous); !errors.Is(err, ErrSession) {
			t.Fatal("the previous session survived successful sign-in")
		}
	}
}

func TestSignInIdentifierRenameBeforeSessionCommitInvalidatesAlias(t *testing.T) {
	m := New(openPasswordTestStore(t))
	u := usernamePolicyRegister(t, m, "fixture-rename@example.test", "Étage")
	anonymous, err := m.Rotate(t.Context(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	verified, err := m.verifyLogin(t.Context(), "étage", "synthetic pantry account password")
	if err != nil {
		t.Fatalf("the unique username never reached the credential fence: %v", err)
	}
	if _, err := m.Update(t.Context(), u.ID, usernamePolicyProfile(u, "Unrelated")); err != nil {
		t.Fatal(err)
	}
	if _, token, err := m.completeLogin(t.Context(), verified, anonymous, true); !errors.Is(err, ErrCredentials) || token != "" {
		t.Fatal("session commit accepted an alias from before its rename")
	}
	if s, err := m.Load(t.Context(), anonymous); err != nil || s.UserID != nil {
		t.Fatal("the rejected attempt changed the anonymous session")
	}
	var row store.User
	if err = m.Store.DB.First(&row, u.ID).Error; err != nil || row.Password != u.Password {
		t.Fatal("the rejected attempt changed the credential", err)
	}
}

func TestSignInIdentifierRenameAfterSessionCommitPreservesNativeAdoption(t *testing.T) {
	m := New(openPasswordTestStore(t))
	u := usernamePolicyRegister(t, m, "fixture-native-rename@example.test", "Étage")
	started, err := m.NativeStart(t.Context(), "ios")
	if err != nil {
		t.Fatal(err)
	}
	old, err := m.NativeLoad(t.Context(), started.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	_, anchor, err := m.LoginSession(t.Context(), "étage", "synthetic pantry account password", started.AccessToken)
	if err != nil {
		t.Fatalf("the unique username never committed a native anchor: %v", err)
	}
	if _, err = m.Update(t.Context(), u.ID, usernamePolicyProfile(u, "New name")); err != nil {
		t.Fatal(err)
	}
	bound, err := m.NativeBind(t.Context(), anchor, "ios", old.FamilyID)
	if err != nil {
		t.Fatal("a later rename invalidated an established native anchor", err)
	}
	got, err := m.NativeLoad(t.Context(), bound.AccessToken)
	if err != nil || got.Session.UserID == nil || *got.Session.UserID != u.ID {
		t.Fatal("native adoption did not preserve the committed account", err)
	}
}

func TestSignInIdentifierAmbiguousLegacyNameNeedsEmailUntilResolved(t *testing.T) {
	m := New(openPasswordTestStore(t))
	one := usernamePolicyRegister(t, m, "first@example.test", "Étage")
	two := store.User{Email: "second@example.test", Username: "étage", FirstName: "Legacy", LastName: "Cook", Password: one.Password}
	if err := m.Store.DB.Create(&two).Error; err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"Étage", "étage", "ÉTAGE"} {
		if _, err := m.Login(t.Context(), alias, "synthetic pantry account password"); !errors.Is(err, ErrCredentials) {
			t.Fatal("ambiguous username selected a legacy account", err)
		}
	}
	for _, u := range []store.User{one, two} {
		got, err := m.Login(t.Context(), u.Email, "synthetic pantry account password")
		if err != nil || got.ID != u.ID {
			t.Fatal("legacy collision blocked independent email sign-in", err)
		}
	}
	if _, err := m.Update(t.Context(), two.ID, usernamePolicyProfile(two, "Elsewhere")); err != nil {
		t.Fatal(err)
	}
	got, err := m.Login(t.Context(), "étage", "synthetic pantry account password")
	if err != nil || got.ID != one.ID {
		t.Fatal("resolving the collision did not restore the remaining alias", err)
	}
}

func TestSignInIdentifierUnresolvedWorkUsesTheExistingPasswordEnvelope(t *testing.T) {
	m := New(openPasswordTestStore(t))
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, fixture := range []struct {
		identifier string
		password   string
		want       error
	}{
		{"missing-username", "synthetic fixture password", context.Canceled},
		{"missing@example.test", "synthetic fixture password", context.Canceled},
		{"malformed@", "synthetic fixture password", ErrCredentials},
		{"contains\x00nul", "synthetic fixture password", ErrCredentials},
		{"missing-username", "", ErrCredentials},
	} {
		target, err := m.ResolveLogin(t.Context(), fixture.identifier)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifyResolvedLogin(canceled, target, fixture.password); !errors.Is(err, fixture.want) {
			t.Fatalf("credential path did not use the expected work boundary: got %v; want %v", err, fixture.want)
		}
	}
}

func TestSignInIdentifierRejectsAuthorityChangesBeforeLegacyRehash(t *testing.T) {
	ctx := t.Context()
	for _, change := range []string{"new ambiguity", "deleted", "changed mailbox", "changed password"} {
		t.Run(change, func(t *testing.T) {
			s := openPasswordTestStore(t)
			m := New(s)
			const password = "legacy pantry test password"
			u := addLegacyUser(t, s, "legacy-cook", password, bcrypt.MinCost)
			anonymous, err := m.Rotate(ctx, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			verified, err := m.verifyLogin(ctx, "  LEGACY-COOK ", password)
			if err != nil || verified.upgrade == "" {
				t.Fatalf("fixture did not produce a pending legacy rehash: %v", err)
			}
			wantPassword := u.Password
			switch change {
			case "new ambiguity":
				err = s.DB.Create(&store.User{Email: "other-cook@example.test", Username: "LEGACY-COOK", FirstName: "Other", LastName: "Cook", Password: u.Password}).Error
			case "deleted":
				err = s.DB.Delete(&u).Error
			case "changed mailbox":
				err = s.DB.Model(&u).Updates(map[string]any{"email": "moved@example.test", "email_key": "moved@example.test"}).Error
			case "changed password":
				wantPassword, err = newPasswordHash(ctx, "new pantry test password")
				if err == nil {
					err = s.DB.Model(&u).Update("password", wantPassword).Error
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, token, err := m.completeLogin(ctx, verified, anonymous, true); !errors.Is(err, ErrCredentials) || token != "" {
				t.Fatal("stale alias verification granted a session")
			}
			if session, err := m.Load(ctx, anonymous); err != nil || session.UserID != nil {
				t.Fatal("stale verification rotated anonymous authority")
			}
			var anchors int64
			if err := s.DB.Model(&store.Session{}).Where("user_id=?", u.ID).Count(&anchors).Error; err != nil || anchors != 0 {
				t.Fatal("stale verification left an authenticated anchor")
			}
			if change != "deleted" {
				var found store.User
				if err := s.DB.First(&found, u.ID).Error; err != nil || found.Password != wantPassword {
					t.Fatal("stale verification changed the live password")
				}
			}
		})
	}
}

func TestSignInIdentifierRejectsPaddedAndNulLegacyAliases(t *testing.T) {
	m := New(openPasswordTestStore(t))
	const password = "legacy pantry test password"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{" Padded ", "Nul\x00name", "invalid@"} {
		user := store.User{Email: fmt.Sprintf("legacy-%d@example.test", i), Username: name, Password: string(hash), FirstName: "Legacy", LastName: "Cook"}
		if err := m.Store.DB.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := m.Login(t.Context(), name, password); !errors.Is(err, ErrCredentials) {
			t.Fatal("disallowed legacy username worked", err)
		}
		got, err := m.Login(t.Context(), user.Email, password)
		if err != nil || got.ID != user.ID {
			t.Fatal("email could not reach the same legacy account", err)
		}
	}
}
