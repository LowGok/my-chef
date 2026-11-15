package store

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/migrations"
)

func shoppingStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "shopping.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, u := range []User{{ID: 1, FirstName: "One", LastName: "Chef", Email: "one@test.invalid", Username: "one", Password: "hash"}, {ID: 2, FirstName: "Two", LastName: "Chef", Email: "two@test.invalid", Username: "two", Password: "hash"}} {
		if err = s.DB.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
	}
	return s
}
func shoppingMeal(t *testing.T, s *Store, owner int64, date, name, quantity string) Meal {
	t.Helper()
	m := Meal{CreatorID: owner, CreationDate: date, RecipeTitle: name, ImageStatus: "unavailable"}
	if err := s.DB.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	i := Ingredient{RecipeID: m.ID, Name: name, Quantity: Ptr(quantity), Unit: Ptr("g")}
	if err := s.DB.Create(&i).Error; err != nil {
		t.Fatal(err)
	}
	return m
}
func TestShoppingPersistenceReconciliationAndConflicts(t *testing.T) {
	s := shoppingStore(t)
	ctx := context.Background()
	first := shoppingMeal(t, s, 1, "2026-11-02", "carrot", "100")
	second := shoppingMeal(t, s, 1, "2026-11-03", "carrot", "200")
	shoppingMeal(t, s, 2, "2026-11-02", "private chocolate", "500")
	list, err := s.Shopping(ctx, 1, "2026-11-02")
	if err != nil || len(list.Items) != 1 || *list.Items[0].Quantity != "300" {
		t.Fatalf("load: %+v %v", list, err)
	}
	list.Items[0].State = "basket"
	list.Items = append(list.Items, domain.ShoppingItem{ID: "extra-1", Name: "Dish soap", Quantity: Ptr("1"), Unit: "bottle", Manual: true, State: "home"})
	list, err = s.SaveShopping(ctx, 1, "2026-11-02", list)
	if err != nil {
		t.Fatal(err)
	}
	saved := list
	excluded := list.Meals[1].Position
	list.Meals[1].Selected = false
	list, err = s.SaveShopping(ctx, 1, "2026-11-02", list)
	if err != nil || len(list.Items) != 2 || *list.Items[0].Quantity != "100" || list.Items[0].State != "buy" || !list.Items[0].Updated {
		t.Fatalf("selection: %+v %v", list, err)
	}
	if _, err = s.SaveShopping(ctx, 1, list.Week, saved); !errors.Is(err, ErrShoppingConflict) {
		t.Fatalf("stale write: %v", err)
	}
	list.Items[0].State = "home"
	list, err = s.SaveShopping(ctx, 1, list.Week, list)
	if err != nil {
		t.Fatal(err)
	}
	// Replacement recreates records, but retained date/ordinal selections are stable.
	if err = s.DB.Delete(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Delete(&second).Error; err != nil {
		t.Fatal(err)
	}
	shoppingMeal(t, s, 1, "2026-11-02", "carrot", "100")
	shoppingMeal(t, s, 1, "2026-11-03", "carrot", "999")
	list, err = s.Shopping(ctx, 1, list.Week)
	if err != nil || list.Items[0].State != "home" || *list.Items[0].Quantity != "100" || list.Meals[1].Position != excluded || list.Meals[1].Selected {
		t.Fatalf("replacement: %+v %v", list, err)
	}
	if len(list.Items) != 2 || !list.Items[1].Manual || list.Items[1].ID != "extra-1" {
		t.Fatal("extra lost")
	}
	// A late client may not put an old amount in the basket after a source update.
	stale := list
	shoppingMeal(t, s, 1, "2026-11-04", "carrot", "50")
	if _, err = s.SaveShopping(ctx, 1, list.Week, stale); !errors.Is(err, ErrShoppingConflict) {
		t.Fatalf("source conflict: %v", err)
	}
	list, err = s.Shopping(ctx, 1, list.Week)
	if err != nil || *list.Items[0].Quantity != "150" || list.Items[0].State != "buy" || !list.Items[0].Updated {
		t.Fatalf("changed source: %+v %v", list, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	after, err := reopened.Shopping(ctx, 1, list.Week)
	if err != nil || after.Revision != list.Revision || len(after.Items) != 2 {
		t.Fatalf("restart: %+v %v", after, err)
	}
	other, err := reopened.Shopping(ctx, 2, list.Week)
	if err != nil || len(other.Items) != 1 || other.Items[0].Name != "private chocolate" {
		t.Fatalf("isolation: %+v %v", other, err)
	}
	if _, err = reopened.Shopping(ctx, 1, "2026-11-03"); !errors.Is(err, ErrShoppingInput) {
		t.Fatal("non-Monday accepted")
	}
}

func TestShoppingUpgradeFromInitialSchema(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "old.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err = s.SQL.Exec(migrations.Initial); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SQL.Exec("CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,dirty BOOLEAN NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SQL.Exec("INSERT INTO schema_migrations VALUES(1,?,0)", schemaChecksum()); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckSchema(context.Background()); err == nil {
		t.Fatal("old schema reported ready")
	}
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func shoppingRecipe(name, quantity string) domain.RecipeContent {
	q := domain.Decimal(quantity)
	return domain.RecipeContent{Title: name, Note: "A saved meal", Yield: []string{"2 servings"}, Equipment: "Pan", Instructions: []string{"Cook and serve"}, Tips: []string{}, Allergens: []string{}, AllergenNote: "Check labels", PrepMinutes: 5, CookMinutes: 10, TotalMinutes: 15, Ingredients: []domain.IngredientContent{{Name: name, Quantity: &q, Unit: Ptr("g")}}}
}
func TestShoppingFollowsRealWeeklyReplacement(t *testing.T) {
	s := shoppingStore(t)
	ctx := context.Background()
	save := func(key string, dates []string, recipes []domain.RecipeContent) domain.Menu {
		t.Helper()
		op := Generation{UserID: 1, Key: key, RequestHash: key, RequestJSON: "{}", Status: "running", Stage: "saving"}
		if err := s.DB.Create(&op).Error; err != nil {
			t.Fatal(err)
		}
		input := domain.MenuInput{Dates: dates, Adults: 2, MaxMinutes: 30, Budget: "low", Region: "Belgium", ReplaceExisting: true}
		result, err := s.SaveMenu(ctx, 1, op.ID, input, recipes)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := save("initial", []string{"2026-11-02", "2026-11-03", "2026-11-04"}, []domain.RecipeContent{shoppingRecipe("carrot", "100"), shoppingRecipe("onion", "50"), shoppingRecipe("paprika", "2")})
	shoppingMeal(t, s, 1, "2026-11-02", "garlic", "10")
	shoppingMeal(t, s, 1, "2026-11-09", "next week", "20")
	other, err := s.Shopping(ctx, 1, "2026-11-09")
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.Shopping(ctx, 1, "2026-11-02")
	if err != nil {
		t.Fatal(err)
	}
	list.Meals[1].Selected = false
	list.Meals[2].Selected = false
	for i, item := range list.Items {
		if item.Name == "carrot" {
			list.Items[i].State = "home"
		}
	}
	list.Items = append(list.Items, domain.ShoppingItem{ID: "soap", Name: "Soap", Manual: true, State: "buy", Contributions: []domain.ShoppingContribution{}})
	list, err = s.SaveShopping(ctx, 1, list.Week, list)
	if err != nil {
		t.Fatal(err)
	}
	replaced := save("replacement", []string{"2026-11-02", "2026-11-05"}, []domain.RecipeContent{shoppingRecipe("carrot", "100"), shoppingRecipe("beans", "20")})
	if replaced.Recipes[0].ID == first.Recipes[0].ID {
		t.Fatal("replacement retained recipe IDs")
	}
	list, err = s.Shopping(ctx, 1, list.Week)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Meals) != 2 || !list.Meals[0].Selected || !list.Meals[1].Selected || len(list.Items) != 3 {
		t.Fatalf("positions not reconciled: %+v", list)
	}
	for i, item := range list.Items {
		if item.Name == "carrot" && item.State != "home" {
			t.Fatal("unchanged carrot lost home state")
		}
		if item.Name == "beans" {
			list.Items[i].State = "basket"
		}
	}
	list, err = s.SaveShopping(ctx, 1, list.Week, list)
	if err != nil {
		t.Fatal(err)
	}
	save("changed-amount", []string{"2026-11-02", "2026-11-05"}, []domain.RecipeContent{shoppingRecipe("carrot", "150"), shoppingRecipe("beans", "20")})
	list, err = s.Shopping(ctx, 1, list.Week)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Items {
		if item.Name == "carrot" && (item.State != "buy" || !item.Updated || *item.Quantity != "150") {
			t.Fatal("changed amount retained old state")
		}
		if item.Name == "beans" && item.State != "basket" {
			t.Fatal("unchanged identity lost basket")
		}
	}
	shoppingMeal(t, s, 1, "2026-11-02", "garlic", "10")
	list, err = s.Shopping(ctx, 1, list.Week)
	if err != nil {
		t.Fatal(err)
	}
	if list.Meals[1].Position != "2026-11-02/1" || !list.Meals[1].Selected {
		t.Fatal("removed ordinal exclusion survived into a new position")
	}
	unchanged, err := s.Shopping(ctx, 1, other.Week)
	if err != nil || unchanged.Revision != other.Revision {
		t.Fatal("replacement changed another week")
	}
}

func TestShoppingDowngradeAppliesOnlyChecksAndRetainsCustomChoices(t *testing.T) {
	s := shoppingStore(t)
	ctx := t.Context()
	const week = "2026-11-02"
	shoppingMeal(t, s, 1, week, "carrot", "100")
	shoppingMeal(t, s, 1, "2026-11-04", "leek", "50")
	var premium atomic.Bool
	premium.Store(true)
	policy := &PlanningPolicy{
		Now:           func() time.Time { return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC) },
		HouseholdZone: func(context.Context, *gorm.DB, int64) (string, error) { return "Europe/Brussels", nil },
		Entitlements: func(context.Context, *gorm.DB, int64, string) (PersonalEntitlement, error) {
			return PersonalEntitlement{Premium: premium.Load()}, nil
		},
	}
	read := func() domain.ShoppingList {
		t.Helper()
		list, err := s.ShoppingForPlan(ctx, 1, week, policy)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	save := func(in domain.ShoppingList) domain.ShoppingList {
		t.Helper()
		list, err := s.SaveShoppingForPlan(ctx, 1, week, in, policy)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	index := func(list domain.ShoppingList, name string) int {
		return slices.IndexFunc(list.Items, func(item domain.ShoppingItem) bool { return item.Name == name })
	}
	before := read()
	before.Meals[1].Selected = false
	before.Items = append(before.Items, domain.ShoppingItem{ID: "retained-extra", Name: "Dish soap", Manual: true, State: "buy"})
	saved := save(before)
	if index(saved, "leek") >= 0 || saved.Meals[1].Selected {
		t.Fatal("Premium could not exclude a dinner")
	}

	premium.Store(false)
	free := read()
	if !free.CustomizationLocked || !free.Meals[1].Selected || index(free, "leek") < 0 || index(free, "Dish soap") < 0 {
		t.Fatal("Free view did not restore all week ingredients while keeping extras", free)
	}
	if _, err := s.SaveShoppingForPlan(ctx, 1, week, saved, policy); !errors.Is(err, ErrShoppingConflict) {
		t.Fatal("offline snapshot bypassed the new scope", err)
	}
	free.Meals[1].Selected = false
	free.Items[index(free, "Dish soap")].State = "basket"
	free.Items[index(free, "carrot")].State = "home"
	free.Items[index(free, "leek")].State = "basket"
	free.Items = append(free.Items, domain.ShoppingItem{ID: "unapproved-extra", Name: "New extra", Manual: true, State: "buy"})
	out := save(free)
	if !slices.Equal(out.BlockedChanges, []string{"extras", "meals"}) || !out.Meals[1].Selected || index(out, "New extra") >= 0 ||
		out.Items[index(out, "Dish soap")].State != "basket" ||
		out.Items[index(out, "carrot")].State != "home" ||
		out.Items[index(out, "leek")].State != "basket" {
		t.Fatal("a forbidden edit was applied or a valid check-off was lost", out)
	}
	out = read()
	if out.Items[index(out, "leek")].State != "basket" {
		t.Fatal("a confirmed check-off did not survive another read")
	}
	premium.Store(true)
	restored := read()
	if restored.CustomizationLocked || restored.Meals[1].Selected || index(restored, "leek") >= 0 ||
		restored.Items[index(restored, "Dish soap")].State != "basket" ||
		restored.Items[index(restored, "carrot")].State != "home" {
		t.Fatal("original Premium choices or shared check-offs were lost", restored)
	}
	premium.Store(false)
	again := read()
	if again.Items[index(again, "leek")].State != "basket" {
		t.Fatal("full-week-only checks were not retained across the tier change")
	}
}
