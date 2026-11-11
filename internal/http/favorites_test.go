package httpapi

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/store"
)

func TestFavoriteHTTPIsolationCSRFAndQueue(t *testing.T) {
	a, h := testAPI(t)
	ctx := context.Background()
	owner, err := a.Auth.Register(ctx, auth.Registration{Email: "fav@test.invalid", Password: "ChefTest2026!", Username: "fav", FirstName: "Fav", LastName: "Chef"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.Auth.Rotate(ctx, &owner.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	m := store.Meal{CreatorID: owner.ID, CreationDate: "2026-11-02", RecipeTitle: "Saved carrots", ImageStatus: "unavailable"}
	if err = a.Store.DB.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/recipes/%d/favorite", m.ID)
	if w := call(h, "PUT", path, "{}", "", "", a.Config.Origin); w.Code != 401 {
		t.Fatal("unauthenticated", w.Code)
	}
	if w := call(h, "PUT", path, "{}", token, "bad", a.Config.Origin); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	if w := call(h, "PUT", path, "{}", token, auth.CSRF(token), a.Config.Origin); w.Code != 200 {
		t.Fatal("save", w.Code, w.Body.String())
	}
	if w := call(h, "GET", "/api/v1/favorites?limit=1", "", token, "", ""); w.Code != 200 {
		t.Fatal("list", w.Code)
	}
	foreign, err := a.Auth.Register(ctx, auth.Registration{Email: "otherfav@test.invalid", Password: "ChefTest2026!", Username: "otherfav", FirstName: "Other", LastName: "Chef"})
	if err != nil {
		t.Fatal(err)
	}
	otherToken, _ := a.Auth.Rotate(ctx, &foreign.ID, "")
	if w := call(h, "PUT", path, "{}", otherToken, auth.CSRF(otherToken), a.Config.Origin); w.Code != 404 {
		t.Fatal("foreign save", w.Code)
	}
	now := time.Now().UTC()
	monday := now.AddDate(0, 0, -((int(now.Weekday()) + 6) % 7))
	week := monday.AddDate(0, 0, 7).Format(time.DateOnly)
	queue := fmt.Sprintf("/api/v1/favorites/%d/queue", m.ID)
	body := fmt.Sprintf(`{"week":%q,"today":%q}`, week, now.Format(time.DateOnly))
	for _, p := range []string{queue, fmt.Sprintf("/api/v1/favorites/%d/swap", m.ID)} {
		if w := call(h, "POST", p, body, token, "bad", a.Config.Origin); w.Code != 403 {
			t.Fatal("action csrf", w.Code)
		}
	}
	if w := call(h, "POST", queue, body, otherToken, auth.CSRF(otherToken), a.Config.Origin); w.Code != 404 {
		t.Fatal("foreign queue", w.Code)
	}
	for range 2 {
		if w := call(h, "POST", queue, body, token, auth.CSRF(token), a.Config.Origin); w.Code != 200 {
			t.Fatal("queue", w.Code, w.Body.String())
		}
	}
	if w := call(h, "DELETE", path, "{}", otherToken, auth.CSRF(otherToken), a.Config.Origin); w.Code != 404 {
		t.Fatal("foreign remove", w.Code)
	}
	if w := call(h, "GET", "/api/v1/favorites?limit=0", "", token, "", ""); w.Code != 400 {
		t.Fatal("invalid pagination", w.Code)
	}
}
