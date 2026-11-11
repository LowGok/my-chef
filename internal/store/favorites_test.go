package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/migrations"
	"gorm.io/gorm"
)

func TestFavoriteSnapshotQueueAndArchive(t *testing.T) {
	s := shoppingStore(t)
	ctx := context.Background()
	m := shoppingMeal(t, s, 1, "2026-11-02", "Carrot supper", "100.123456789")
	original, err := s.SetFavorite(ctx, 1, m.ID, true)
	if err != nil || !original.IsFavorite {
		t.Fatal(original, err)
	}
	if _, err = s.SetFavorite(ctx, 2, m.ID, true); err == nil {
		t.Fatal("foreign favorite accepted")
	}
	for range 2 {
		if _, err = s.QueueFavorite(ctx, 1, m.ID, "2026-11-09"); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := s.FavoriteWeek(ctx, 1, "2026-11-09")
	if err != nil || len(pending.Recipes) != 1 {
		t.Fatal(pending, err)
	}
	if err = s.DB.Delete(&m).Error; err != nil {
		t.Fatal(err)
	}
	archived, err := s.GetRecipe(ctx, 1, m.ID)
	if err != nil || *archived.Ingredients[0].Quantity != "100.123456789" {
		t.Fatal(archived, err)
	}
	if _, err = s.SetFavorite(ctx, 1, m.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetFavorite(ctx, 1, m.ID, false); err != nil {
		t.Fatal("archived remove retry is not idempotent", err)
	}
	if _, err = s.GetRecipe(ctx, 1, m.ID); err == nil {
		t.Fatal("removed archived recipe still readable")
	}
	pending, err = s.FavoriteWeek(ctx, 1, "2026-11-09")
	if err != nil || len(pending.Recipes) != 1 || pending.Recipes[0].Title != original.Title {
		t.Fatal("queue lost snapshot", err)
	}
	reopened, err := Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.FavoriteWeek(ctx, 1, "2026-11-09")
	if err != nil || len(got.Recipes) != 1 {
		t.Fatal("queue not persistent", err)
	}
}
func TestFavoriteSwapIsAtomicOwnedAndIdempotent(t *testing.T) {
	s := shoppingStore(t)
	ctx := context.Background()
	source := shoppingMeal(t, s, 1, "2026-10-01", "Favorite", "250")
	target := shoppingMeal(t, s, 1, "2026-11-02", "Old", "100")
	other := shoppingMeal(t, s, 1, "2026-11-03", "Keep", "200")
	if _, err := s.SetFavorite(ctx, 1, source.ID, true); err != nil {
		t.Fatal(err)
	}
	input := domain.FavoriteSwap{Week: "2026-11-02", TargetID: strconv.FormatInt(target.ID, 10), RequestKey: "swap-test-key"}
	first, err := s.SwapFavorite(ctx, 1, source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.SwapFavorite(ctx, 1, source.ID, input)
	if err != nil || again.ID != first.ID {
		t.Fatal("duplicate swap", err)
	}
	if _, err = s.GetRecipe(ctx, 1, other.ID); err != nil {
		t.Fatal("other meal deleted", err)
	}
	if first.Date != "2026-11-02" || *first.Ingredients[0].Quantity != "250" {
		t.Fatal(first)
	}
	input.RequestKey = "foreign-swap-key"
	input.TargetID = strconv.FormatInt(other.ID, 10)
	if _, err = s.SwapFavorite(ctx, 2, source.ID, input); err == nil {
		t.Fatal("foreign swap accepted")
	}
	if err = s.DB.Exec("CREATE TRIGGER fail_favorite_ingredient BEFORE INSERT ON ingredients BEGIN SELECT RAISE(ABORT, 'test rollback'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.SwapFavorite(ctx, 1, source.ID, input); err == nil {
		t.Fatal("failed ingredient accepted")
	}
	if _, err = s.GetRecipe(ctx, 1, other.ID); err != nil {
		t.Fatal("failed swap lost original", err)
	}
}
func TestFavoriteQueueBoundAndPagination(t *testing.T) {
	s := shoppingStore(t)
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		m := shoppingMeal(t, s, 1, "2026-11-02", "Meal "+strconv.Itoa(i), "1")
		if _, err := s.SetFavorite(ctx, 1, m.ID, true); err != nil {
			t.Fatal(err)
		}
		_, err := s.QueueFavorite(ctx, 1, m.ID, "2026-11-09")
		if (i < 7) != (err == nil) {
			t.Fatal("queue cap", i, err)
		}
	}
	page, err := s.Favorites(ctx, 1, 3, 0)
	if err != nil || len(page.Recipes) != 3 || page.NextOffset == nil {
		t.Fatal(page, err)
	}
	next, err := s.Favorites(ctx, 1, 3, *page.NextOffset)
	if err != nil || len(next.Recipes) != 3 || next.Recipes[0].ID == page.Recipes[0].ID {
		t.Fatal(next, err)
	}
	foreign, err := s.Favorites(ctx, 2, 20, 0)
	if err != nil || len(foreign.Recipes) != 0 {
		t.Fatal(foreign, err)
	}
}
func TestFavoriteSaveMenuConsumesOnlyCapturedQueueAndRollsBack(t *testing.T) {
	s := shoppingStore(t)
	ctx := context.Background()
	m := shoppingMeal(t, s, 1, "2026-10-01", "Saved", "40")
	_, _ = s.SetFavorite(ctx, 1, m.ID, true)
	_, _ = s.QueueFavorite(ctx, 1, m.ID, "2026-11-09")
	week, _ := s.FavoriteWeek(ctx, 1, "2026-11-09")
	in := domain.MenuInput{Dates: []string{"2026-11-09"}, Adults: 2, MaxMinutes: 30, Budget: "low", Region: "Belgium", Creativity: 5, FavoriteRevisions: map[string]string{"2026-11-09": week.Revision}}
	var plan []domain.PlannedFavorite
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		var e error
		plan, e = CaptureFavorites(tx, 1, in, func([]string) {})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(domain.GenerationRequest{MenuInput: in, Favorites: plan})
	op := Generation{UserID: 1, Key: "queue-save", RequestHash: "hash", RequestJSON: string(raw), Status: "running", Stage: "saving"}
	if err = s.DB.Create(&op).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Exec("CREATE TRIGGER reject_fav_save BEFORE INSERT ON ingredients BEGIN SELECT RAISE(ABORT, 'failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveMenu(ctx, 1, op.ID, in, nil); err == nil {
		t.Fatal("expected rollback")
	}
	pending, _ := s.FavoriteWeek(ctx, 1, "2026-11-09")
	if len(pending.Recipes) != 1 {
		t.Fatal("lost queued favorite")
	}
	_ = s.DB.Exec("DROP TRIGGER reject_fav_save").Error
	menu, err := s.SaveMenu(ctx, 1, op.ID, in, nil)
	if err != nil || len(menu.Recipes) != 1 || menu.Recipes[0].Title != "Saved" {
		t.Fatal(menu, err)
	}
	pending, _ = s.FavoriteWeek(ctx, 1, "2026-11-09")
	if len(pending.Recipes) != 0 {
		t.Fatal("queue not consumed")
	}
}

func TestFavoriteSwapBlocksActiveGenerationAndReconcilesShopping(t *testing.T) {
	s := shoppingStore(t)
	ctx := context.Background()
	source := shoppingMeal(t, s, 1, "2026-10-01", "leek", "75")
	target := shoppingMeal(t, s, 1, "2026-11-02", "carrot", "100")
	_, err := s.SetFavorite(ctx, 1, source.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.Shopping(ctx, 1, "2026-11-02")
	if err != nil {
		t.Fatal(err)
	}
	list.Items = append(list.Items, domain.ShoppingItem{ID: "extra-1", Name: "Soap", Quantity: Ptr("1"), Unit: "bottle", Manual: true, State: "home"})
	if _, err = s.SaveShopping(ctx, 1, list.Week, list); err != nil {
		t.Fatal(err)
	}
	input := domain.FavoriteSwap{Week: list.Week, TargetID: strconv.FormatInt(target.ID, 10), RequestKey: "serialized-swap"}
	raw, _ := json.Marshal(domain.GenerationRequest{MenuInput: domain.MenuInput{Dates: []string{"2026-11-02"}}})
	op := Generation{UserID: 1, Key: "active-week", RequestHash: "hash", RequestJSON: string(raw), Status: "queued", Stage: "queued"}
	if err = s.DB.Create(&op).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.SwapFavorite(ctx, 1, source.ID, input); err == nil {
		t.Fatal("swap raced active generation")
	}
	if _, err = s.GetRecipe(ctx, 1, target.ID); err != nil {
		t.Fatal("blocked swap deleted meal")
	}
	if err = s.DB.Model(&op).Update("status", "failed").Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.SwapFavorite(ctx, 1, source.ID, input); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Shopping(ctx, 1, list.Week)
	if err != nil {
		t.Fatal(err)
	}
	foundExtra, foundLeek := false, false
	for _, item := range updated.Items {
		if item.Name == "Soap" && item.State == "home" {
			foundExtra = true
		}
		if item.Name == "leek" && *item.Quantity == "75" {
			foundLeek = true
		}
	}
	if !foundExtra || !foundLeek {
		t.Fatal("shopping did not reconcile", updated)
	}
}

func TestFavoriteMigrationUpgradesShoppingDatabaseWithoutChangingMeals(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "v2.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err = s.SQL.Exec(migrations.Initial + migrations.Shopping); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SQL.Exec("CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,dirty BOOLEAN NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SQL.Exec("INSERT INTO schema_migrations VALUES(2,?,0)", shoppingSchemaChecksum()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SQL.Exec("INSERT INTO users(user_id,firstname,lastname,email,username,password) VALUES(1,'Keep','Chef','upgrade@test.invalid','upgrade','hash')"); err != nil {
		t.Fatal(err)
	}
	// The version-two fixture predates the personal provenance column.
	meal := Meal{CreatorID: 1, CreationDate: "2026-11-02", RecipeTitle: "Existing", ImageStatus: "unavailable"}
	if err := s.DB.Omit("PersonalSource").Create(&meal).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&Ingredient{RecipeID: meal.ID, Name: "Existing", Quantity: Ptr("12"), Unit: Ptr("g")}).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetRecipe(ctx, 1, meal.ID)
	if err != nil || r.Title != "Existing" {
		t.Fatal(r, err)
	}
	if _, err = s.SetFavorite(ctx, 1, meal.ID, true); err != nil {
		t.Fatal("new schema unavailable", err)
	}
}

func TestFavoriteReviewedWeekDetectsInterveningSwap(t *testing.T) {
	s := shoppingStore(t)
	ctx := context.Background()
	source := shoppingMeal(t, s, 1, "2026-10-01", "Favorite", "20")
	target := shoppingMeal(t, s, 1, "2026-11-02", "Original", "30")
	if _, err := s.SetFavorite(ctx, 1, source.ID, true); err != nil {
		t.Fatal(err)
	}
	reviewed, err := s.FavoriteWeek(ctx, 1, "2026-11-02")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SwapFavorite(ctx, 1, source.ID, domain.FavoriteSwap{Week: reviewed.Week, TargetID: strconv.FormatInt(target.ID, 10), RequestKey: "swap-after-review"}); err != nil {
		t.Fatal(err)
	}
	input := domain.MenuInput{Dates: []string{"2026-11-02"}, FavoriteRevisions: map[string]string{reviewed.Week: reviewed.Revision}, ReplaceExisting: true}
	err = s.DB.Transaction(func(tx *gorm.DB) error { _, e := CaptureFavorites(tx, 1, input, func([]string) {}); return e })
	if !errors.Is(err, ErrFavoriteConflict) {
		t.Fatal("stale planner could overwrite confirmed swap", err)
	}
}
func TestFavoriteQueueRetryAfterFavoriteRemoval(t *testing.T) {
	s := shoppingStore(t)
	ctx := context.Background()
	m := shoppingMeal(t, s, 1, "2026-10-01", "Saved", "20")
	if _, err := s.SetFavorite(ctx, 1, m.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueueFavorite(ctx, 1, m.ID, "2026-11-09"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFavorite(ctx, 1, m.ID, false); err != nil {
		t.Fatal(err)
	}
	pending, err := s.QueueFavorite(ctx, 1, m.ID, "2026-11-09")
	if err != nil || len(pending.Recipes) != 1 {
		t.Fatal("idempotent retry lost confirmed queue result", err)
	}
}
