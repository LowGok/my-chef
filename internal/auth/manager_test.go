package auth

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func TestSessionsRotateExpireAndRevoke(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "auth.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	m := New(s)
	anon, err := m.Rotate(ctx, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	u, err := m.Register(ctx, Registration{Email: "ada@example.test", Password: "ChefTest2026!", Username: "ada", FirstName: "Ada", LastName: "Chef"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Login(ctx, "ada@example.test", "wrong"); err != ErrCredentials {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err = m.Login(ctx, "ada@example.test", "ChefTest2026!"); err != nil {
		t.Fatal(err)
	}
	token, err := m.Rotate(ctx, &u.ID, anon)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Load(ctx, anon); err == nil {
		t.Fatal("anonymous token survived rotation")
	}
	session, err := m.Load(ctx, token)
	if err != nil || session.UserID == nil || *session.UserID != u.ID {
		t.Fatalf("expected a current session: %v", err)
	}
	if err = s.DB.Model(&store.Session{}).Where("hash = ?", Hash(token)).Update("expires_unix", time.Now().Unix()-1).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = m.Load(ctx, token); err == nil {
		t.Fatal("expired token accepted")
	}
	token, err = m.Rotate(ctx, &u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Revoke(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Load(ctx, token); err == nil {
		t.Fatal("revoked token accepted")
	}
	if _, err = m.Register(ctx, Registration{Email: "ada@example.test", Password: "ChefTest2026!", Username: "different", FirstName: "A", LastName: "B"}); err != ErrConflict {
		t.Fatalf("duplicate identity: %v", err)
	}
}

func openPasswordTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "auth.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func addLegacyUser(t *testing.T, s *store.Store, name, password string, cost int) store.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		t.Fatal(err)
	}
	u := store.User{Email: name + "@example.test", Password: string(hash), Username: name, FirstName: name, LastName: "Test"}
	if err := s.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func TestLegacyCredentialsUpgradeWithoutRewritingOthers(t *testing.T) {
	ctx := context.Background()
	s := openPasswordTestStore(t)
	m := New(s)
	const decomposed = "Cafe\u0301abc"
	const composed = "Caféabc"
	legacy := addLegacyUser(t, s, "legacy", decomposed, bcrypt.MinCost)
	other := addLegacyUser(t, s, "other", "short-ok", bcrypt.DefaultCost)
	if _, err := m.Login(ctx, legacy.Email, composed); !errors.Is(err, ErrCredentials) {
		t.Fatal("legacy verification did not use the original submitted bytes")
	}
	user, err := m.Login(ctx, legacy.Email, decomposed)
	if err != nil || !verifyArgon2id(user.Password, composed) {
		t.Fatal("successful sign-in did not upgrade the legacy credential")
	}
	if _, err := m.Login(ctx, legacy.Email, composed); err != nil {
		t.Fatal("normalized credential did not sign in after upgrading")
	}
	var untouched store.User
	if err := s.DB.First(&untouched, other.ID).Error; err != nil || untouched.Password != other.Password {
		t.Fatal("sign-in rewrote an unrelated account")
	}
	high := addLegacyUser(t, s, "highercost", "old higher cost phrase", bcrypt.DefaultCost+1)
	if got, err := m.Login(ctx, high.Email, "old higher cost phrase"); err != nil || got.Password == high.Password {
		t.Fatal("higher-cost legacy sign-in did not upgrade")
	}
	if err := s.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationStoresLongCurrentCredential(t *testing.T) {
	ctx := context.Background()
	m := New(openPasswordTestStore(t))
	r := Registration{Email: "long@example.test", Username: "long", FirstName: "Long", LastName: "Test", Password: "too-short"}
	if _, err := m.Register(ctx, r); !errors.Is(err, ErrPasswordLength) {
		t.Fatal("registration did not enforce the new minimum")
	}
	r.Password = strings.Repeat("😀", 128)
	user, err := m.Register(ctx, r)
	if err != nil || !verifyArgon2id(user.Password, r.Password) {
		t.Fatal("registration did not store a valid long Argon2id credential")
	}
	if _, err := m.Login(ctx, r.Email, r.Password); err != nil {
		t.Fatal("sign-in rejected a registered 512-byte credential")
	}
}

func TestVerifiedLoginCannotOutliveItsCredentialVersion(t *testing.T) {
	ctx := context.Background()
	s := openPasswordTestStore(t)
	m := New(s)
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy_%t", legacy), func(t *testing.T) {
			name := fmt.Sprintf("overlap_%t", legacy)
			const old, replacement = "original test phrase", "replacement test phrase"
			var user store.User
			if legacy {
				user = addLegacyUser(t, s, name, old, bcrypt.MinCost)
			} else {
				var err error
				user, err = m.Register(ctx, Registration{Email: name + "@example.test", Username: name, FirstName: name, LastName: "Test", Password: old})
				if err != nil {
					t.Fatal(err)
				}
			}
			anonymous, err := m.Rotate(ctx, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			verified, err := m.verifyLogin(ctx, user.Email, old)
			if err != nil {
				t.Fatal(err)
			}
			newHash, err := newPasswordHash(ctx, replacement)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.DB.Model(&user).Update("password", newHash).Error; err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.completeLogin(ctx, verified, anonymous, true); !errors.Is(err, ErrCredentials) {
				t.Fatal("old verification created a session after the password changed")
			}
			var sessions int64
			if err := s.DB.Model(&store.Session{}).Where("user_id = ?", user.ID).Count(&sessions).Error; err != nil || sessions != 0 {
				t.Fatal("stale login wrote an authenticated session")
			}
			if _, err := m.Load(ctx, anonymous); err != nil {
				t.Fatal("stale login changed the initiating session")
			}
			if _, err := m.Login(ctx, user.Email, replacement); err != nil {
				t.Fatal("stale legacy rehash overwrote the replacement credential")
			}
		})
	}
}
