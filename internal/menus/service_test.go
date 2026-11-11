package menus_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/store"
)

func testStore(t *testing.T) (*store.Store, store.User, store.Generation) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "menu.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	u := store.User{FirstName: "Ada", LastName: "Chef", Email: "ada@example.test", Username: "ada", Password: "hash"}
	if err = s.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	g := store.Generation{UserID: u.ID, Key: "test-menu", RequestHash: "hash", RequestJSON: "{}", Status: "running", Stage: "generating"}
	if err = s.DB.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	return s, u, g
}
func recipe(name string) domain.RecipeContent {
	q := domain.Decimal("0.12345678901234567890123456789")
	unit := "g"
	return domain.RecipeContent{Title: "Same title", Note: "A weekday recipe", Yield: []string{"2 servings"}, Equipment: "Pan", Instructions: []string{"Simmer gently."}, Tips: []string{"Taste before serving."}, Allergens: []string{}, AllergenNote: "Check ingredient labels.", PrepMinutes: 5, CookMinutes: 10, TotalMinutes: 15, Ingredients: []domain.IngredientContent{{Name: name, Quantity: &q, Unit: &unit}}}
}
func input() domain.MenuInput {
	return domain.MenuInput{Dates: []string{"2026-12-31", "2027-01-01"}, Adults: 2, Children: 0, MaxMinutes: 30, Budget: "low", Region: "Belgium", Creativity: 5}
}

func TestDuplicateTitlesRetainTheirOwnIngredients(t *testing.T) {
	s, u, g := testStore(t)
	menu, err := s.SaveMenu(context.Background(), u.ID, g.ID, input(), []domain.RecipeContent{recipe("carrot"), recipe("leek")})
	if err != nil {
		t.Fatal(err)
	}
	if len(menu.Recipes) != 2 || menu.Recipes[0].ID == menu.Recipes[1].ID || menu.Recipes[0].Ingredients[0].Name != "carrot" || menu.Recipes[1].Ingredients[0].Name != "leek" {
		t.Fatalf("wrong associations: %+v", menu)
	}
	if *menu.Recipes[0].Ingredients[0].Quantity != "0.12345678901234567890123456789" {
		t.Fatal("quantity lost precision")
	}
	page, err := s.History(context.Background(), u.ID, "2026-12-28", "2027-01-04", 25, 0)
	if err != nil || len(page.Recipes) != 2 {
		t.Fatalf("year boundary: %+v %v", page, err)
	}
	page, err = s.History(context.Background(), u.ID, "2026-12-28", "2027-01-01", 25, 0)
	if err != nil || len(page.Recipes) != 1 {
		t.Fatalf("exclusive date bound: %+v %v", page, err)
	}
	if _, err = s.GetMenu(context.Background(), u.ID+1, 1); err == nil {
		t.Fatal("another owner read the menu")
	}
}

func TestIngredientFailureRollsBackTheWholeMenu(t *testing.T) {
	s, u, g := testStore(t)
	if err := s.DB.Exec("CREATE TRIGGER reject_ingredients BEFORE INSERT ON ingredients BEGIN SELECT RAISE(ABORT, 'injected ingredient failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveMenu(context.Background(), u.ID, g.ID, input(), []domain.RecipeContent{recipe("carrot"), recipe("leek")}); err == nil {
		t.Fatal("save unexpectedly succeeded")
	}
	for _, table := range []string{"menus", "meals", "ingredients"} {
		var count int64
		if err := s.DB.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%s retained partial rows: %d %v", table, count, err)
		}
	}
	var after store.User
	if err := s.DB.First(&after, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.LastMeal != nil {
		t.Fatal("lastmeal committed despite rollback")
	}
}
