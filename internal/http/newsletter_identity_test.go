package httpapi

import (
	"context"
	"testing"

	"github.com/LowGok/my-chef/internal/auth"
)

func TestNewsletterContactCannotReadAppProfile(t *testing.T) {
	a, h := testAPI(t)
	ctx := context.Background()
	u, err := a.Auth.Register(ctx, auth.Registration{Email: "http-contact@example.test", Password: "NewsletterFixture2026!", Username: "http-contact", FirstName: "Fixture", LastName: "Contact"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.Auth.Rotate(ctx, &u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.DB.Exec(`UPDATE users SET isNewsletterOnly=1 WHERE user_id=?`, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if w := call(h, "GET", "/api/v1/profile", "", token, "", ""); w.Code != 401 {
		t.Fatalf("contact profile access: %d", w.Code)
	}
}
