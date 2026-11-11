package auth

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

func TestLifecyclePasswordVerificationBindsLiveSessionAndCredential(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "account.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	m := New(s)
	user, err := m.Register(t.Context(), Registration{Email: "actor@example.test", Username: "actor", FirstName: "Synthetic", LastName: "Actor", Password: "Lifecycle actor password!"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := m.Rotate(t.Context(), &user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, password := range []string{"", "wrong"} {
		if _, err := m.VerifyAccountAction(t.Context(), user.ID, token, password); !errors.Is(err, ErrCurrentPassword) {
			t.Fatal("invalid password accepted", err)
		}
	}
	actor, err := m.VerifyAccountAction(t.Context(), user.ID, token, "Lifecycle actor password!")
	if err != nil || actor.UserID != user.ID || actor.SessionHash != Hash(token) || actor.PasswordHash != user.Password {
		t.Fatal("step-up was not bound to exact session and credential", err)
	}
	if err := s.DB.Model(&store.Session{}).Where("hash=?", Hash(token)).Update("expires_unix", time.Now().Add(-time.Minute).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := m.VerifyAccountAction(t.Context(), user.ID, token, "Lifecycle actor password!"); !errors.Is(err, ErrSession) {
		t.Fatal("expired session passed step-up", err)
	}
}
