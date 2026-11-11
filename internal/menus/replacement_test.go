package menus_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/store"
)

func replacementOperation(t *testing.T, s *store.Store, user int64, key string) store.Generation {
	t.Helper()
	op := store.Generation{UserID: user, Key: key, RequestHash: key, RequestJSON: "{}", Status: "running", Stage: "saving"}
	if err := s.DB.Create(&op).Error; err != nil {
		t.Fatal(err)
	}
	return op
}

func TestWeeklyReplacementRequiresConsentAndPreservesOtherWeeksAndOwners(t *testing.T) {
	ctx := context.Background()
	s, user, first := testStore(t)
	old, err := s.SaveMenu(ctx, user.ID, first.ID, input(), []domain.RecipeContent{recipe("carrot"), recipe("leek")})
	if err != nil {
		t.Fatal(err)
	}
	other := store.User{FirstName: "Other", LastName: "Chef", Email: "other@example.test", Username: "other", Password: "hash"}
	if err = s.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range []store.Meal{
		{CreatorID: user.ID, CreationDate: "2026-12-31", RecipeTitle: "Existing duplicate", ImageStatus: "unavailable"},
		{CreatorID: user.ID, CreationDate: "2027-01-05", RecipeTitle: "Next week", ImageStatus: "unavailable"},
		{CreatorID: other.ID, CreationDate: "2026-12-31", RecipeTitle: "Other owner", ImageStatus: "unavailable"},
	} {
		if err = s.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	op := replacementOperation(t, s, user.ID, "replacement")
	request := input()
	request.Dates = []string{"2026-12-30"}
	if _, err = s.SaveMenu(ctx, user.ID, op.ID, request, []domain.RecipeContent{recipe("new carrot")}); err == nil {
		t.Fatal("overwrote an existing week without consent")
	}
	request.ReplaceExisting = true
	newMenu, err := s.SaveMenu(ctx, user.ID, op.ID, request, []domain.RecipeContent{recipe("new carrot")})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.History(ctx, user.ID, "2026-12-28", "2027-01-04", 25, 0)
	if err != nil || len(page.Recipes) != 1 || page.Recipes[0].ID != newMenu.Recipes[0].ID {
		t.Fatalf("duplicate or missing meals: %+v %v", page, err)
	}
	for _, previous := range old.Recipes {
		if _, err = s.GetRecipe(ctx, user.ID, mustID(t, previous.ID)); err == nil {
			t.Fatal("replaced recipe still exists")
		}
		var count int64
		if err = s.DB.Model(&store.Ingredient{}).Where("recipe_id = ?", previous.ID).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("replaced ingredients remain", err)
		}
	}
	for _, owner := range []int64{user.ID, other.ID} {
		page, err = s.History(ctx, owner, "2026-12-28", "2027-01-11", 25, 0)
		want := 1
		if owner == user.ID {
			want = 2
		}
		if err != nil || len(page.Recipes) != want {
			t.Fatalf("unrelated meals changed: %+v %v", page, err)
		}
	}
}

func mustID(t *testing.T, value string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestFailedReplacementRestoresOldMealsAndIngredients(t *testing.T) {
	ctx := context.Background()
	s, user, first := testStore(t)
	old, err := s.SaveMenu(ctx, user.ID, first.ID, input(), []domain.RecipeContent{recipe("carrot"), recipe("leek")})
	if err != nil {
		t.Fatal(err)
	}
	op := replacementOperation(t, s, user.ID, "failed-replacement")
	if err = s.DB.Exec("CREATE TRIGGER reject_replacement BEFORE INSERT ON ingredients BEGIN SELECT RAISE(ABORT, 'injected replacement failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	request := input()
	request.ReplaceExisting = true
	if _, err = s.SaveMenu(ctx, user.ID, op.ID, request, []domain.RecipeContent{recipe("new carrot"), recipe("new leek")}); err == nil {
		t.Fatal("replacement unexpectedly succeeded")
	}
	for _, previous := range old.Recipes {
		row, e := s.GetRecipe(ctx, user.ID, mustID(t, previous.ID))
		if e != nil || len(row.Ingredients) != 1 || row.Ingredients[0].Name != previous.Ingredients[0].Name {
			t.Fatalf("old menu lost during rollback: %+v %v", row, e)
		}
	}
}

func TestReplacementCoversBothWeeksAcrossAYearBoundary(t *testing.T) {
	ctx := context.Background()
	s, user, first := testStore(t)
	request := input()
	request.Dates = []string{"2026-12-31", "2027-01-05"}
	if _, err := s.SaveMenu(ctx, user.ID, first.ID, request, []domain.RecipeContent{recipe("carrot"), recipe("leek")}); err != nil {
		t.Fatal(err)
	}
	op := replacementOperation(t, s, user.ID, "two-week-replacement")
	request.Dates = []string{"2027-01-03", "2027-01-04"}
	request.ReplaceExisting = true
	if _, err := s.SaveMenu(ctx, user.ID, op.ID, request, []domain.RecipeContent{recipe("new carrot"), recipe("new leek")}); err != nil {
		t.Fatal(err)
	}
	page, err := s.History(ctx, user.ID, "2026-12-28", "2027-01-11", 25, 0)
	if err != nil || len(page.Recipes) != 2 || page.Recipes[0].Date != "2027-01-03" || page.Recipes[1].Date != "2027-01-04" {
		t.Fatalf("replacement across weeks: %+v %v", page, err)
	}
}
