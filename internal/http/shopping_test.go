package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/store"
)

func TestShoppingHTTPIsolationAndRevision(t *testing.T) {
	a, h := testAPI(t)
	ctx := context.Background()
	owner, err := a.Auth.Register(ctx, auth.Registration{Email: "shop@test.invalid", Password: "ChefTest2026!", Username: "shop", FirstName: "Shop", LastName: "Chef"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.Auth.Rotate(ctx, &owner.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	m := store.Meal{CreatorID: owner.ID, CreationDate: "2026-11-02", RecipeTitle: "Carrots", ImageStatus: "unavailable"}
	if err = a.Store.DB.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	if err = a.Store.DB.Create(&store.Ingredient{RecipeID: m.ID, Name: "carrot", Quantity: store.Ptr("450"), Unit: store.Ptr("g")}).Error; err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/shopping/2026-11-02"
	if w := call(h, "GET", path, "", "", "", ""); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	w := call(h, "GET", path, "", token, "", "")
	if w.Code != 200 {
		t.Fatalf("shopping unavailable: %d %s", w.Code, w.Body.String())
	}
	var list domain.ShoppingList
	if err = json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatal(list)
	}
	list.Items[0].State = "basket"
	body, _ := json.Marshal(list)
	if w = call(h, "PUT", path, string(body), token, "bad", a.Config.Origin); w.Code != 403 {
		t.Fatal("CSRF bypass")
	}
	if w = call(h, "PUT", path, string(body), token, auth.CSRF(token), a.Config.Origin); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = call(h, "PUT", path, string(body), token, auth.CSRF(token), a.Config.Origin); w.Code != 409 {
		t.Fatal("stale revision accepted")
	}
	if w = call(h, "GET", "/api/v1/shopping/2026-11-03", "", token, "", ""); w.Code != 400 {
		t.Fatal("invalid week accepted")
	}
}
