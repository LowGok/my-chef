package auth

import (
	"context"
	"testing"

	"github.com/LowGok/my-chef/internal/store"
)

func TestNewsletterContactCannotUseAppCredentialsOrSessions(t *testing.T) {
	ctx := context.Background()
	s := openPasswordTestStore(t)
	m := New(s)
	u, err := m.Register(ctx, Registration{Email: "contact@example.test", Password: "NewsletterFixture2026!", Username: "contact", FirstName: "Synthetic", LastName: "Contact"})
	if err != nil {
		t.Fatal(err)
	}
	old, err := m.Rotate(ctx, &u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := m.Rotate(ctx, &u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	native, err := m.NativeBind(ctx, anchor, "ios", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec(`UPDATE users SET isNewsletterOnly=1 WHERE user_id=?`, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := m.Login(ctx, u.Email, "NewsletterFixture2026!"); err == nil {
		t.Fatal("contact logged in")
	}
	if _, err := m.Load(ctx, old); err == nil {
		t.Fatal("contact used a web session")
	}
	if _, err := m.Rotate(ctx, &u.ID, ""); err == nil {
		t.Fatal("contact received a session")
	}
	if _, err := m.NativeLoad(ctx, native.AccessToken); err == nil {
		t.Fatal("contact used native access")
	}
	if _, err := m.NativeRefresh(ctx, native.RefreshToken); err == nil {
		t.Fatal("contact refreshed native access")
	}
	if _, err := m.NativeBind(ctx, old, "ios", ""); err == nil {
		t.Fatal("contact bound a native session")
	}
	var contact store.User
	if err := s.DB.First(&contact, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if contact.ID != u.ID {
		t.Fatal("contact identity changed")
	}
}
