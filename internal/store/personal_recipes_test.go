package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"gorm.io/gorm"
)

func personalFixture(t *testing.T) (*Store, PersonalActor, PersonalActor, PersonalEntitlementReader, time.Time) {
	t.Helper()
	s := shoppingStore(t)
	now := time.Now().UTC()
	if err := s.DB.Model(&User{}).Where("user_id IN ?", []int64{1, 2}).Update("email_verified_at", now.Unix()).Error; err != nil {
		t.Fatal(err)
	}
	for id := int64(1); id <= 2; id++ {
		if err := s.DB.Create(&Session{Hash: fmt.Sprintf("personal-session-%d", id), UserID: &id, ExpiresUnix: now.Add(time.Hour).Unix()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	reader := func(context.Context, *gorm.DB, int64, string) (PersonalEntitlement, error) {
		return PersonalEntitlement{Premium: true, ExpiresUnix: now.Add(time.Hour).Unix()}, nil
	}
	return s, PersonalActor{UserID: 1, SessionHash: "personal-session-1"}, PersonalActor{UserID: 2, SessionHash: "personal-session-2"}, reader, now
}

func personalDocument(title string) domain.PersonalRecipeContent {
	q := domain.Decimal("12345678901234567890.00000000001")
	return domain.PersonalRecipeContent{SchemaVersion: 1, Title: title, Servings: 4, PrepMinutes: 5, CookMinutes: 10, TotalMinutes: 20,
		Ingredients: []domain.IngredientContent{{Name: "Carrot", Quantity: &q}}, Instructions: []string{"Cook gently."}}
}

func TestPersonalStoreOwnedVersionsConflictSearchAndRestart(t *testing.T) {
	s, actor, other, premium, now := personalFixture(t)
	ctx := t.Context()
	input := PersonalSave{RequestKey: strings.Repeat("a", 32), Content: personalDocument("Private 100% dinner_")}
	first, err := s.SavePersonalRecipe(ctx, actor, premium, input, now)
	if err != nil || first.ID == "" || first.VersionID == "" || first.CurrentVersionID != first.VersionID {
		t.Fatal("initial save failed", err)
	}
	replay, err := s.SavePersonalRecipe(ctx, actor, premium, input, now)
	if err != nil || replay.VersionID != first.VersionID || replay.ID != first.ID {
		t.Fatal("save retry created another version", err)
	}
	input.Content.Title = "Another body"
	if _, err := s.SavePersonalRecipe(ctx, actor, premium, input, now); !errors.Is(err, ErrPersonalKeyConflict) {
		t.Fatal("request key accepted different content", err)
	}
	update := PersonalSave{ID: first.ID, ExpectedVersion: first.VersionID, RequestKey: strings.Repeat("b", 32), Content: personalDocument("New title")}
	second, err := s.SavePersonalRecipe(ctx, actor, premium, update, now.Add(time.Second))
	if err != nil || second.VersionID == first.VersionID {
		t.Fatal("new version missing", err)
	}
	if _, err := s.ReadPersonalRecipe(ctx, other, first.ID, "", now); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatal("foreign recipe readable", err)
	}
	if _, err := s.ReadPersonalRecipe(ctx, other, first.ID, first.VersionID, now); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatal("foreign version readable", err)
	}
	old, err := s.ReadPersonalRecipe(ctx, actor, first.ID, first.VersionID, now)
	if err != nil || old.Content.Title != "Private 100% dinner_" || *old.Content.Ingredients[0].Quantity != "12345678901234567890.00000000001" {
		t.Fatal("old version changed", err)
	}
	if err := s.DB.Exec("UPDATE personal_recipe_versions SET body='{}' WHERE id=?", first.VersionID).Error; err == nil {
		t.Fatal("immutable version was updated")
	}
	update.RequestKey, update.Content.Title = strings.Repeat("c", 32), "Unsaved input"
	_, err = s.SavePersonalRecipe(ctx, actor, premium, update, now)
	var conflict *PersonalVersionConflict
	if !errors.As(err, &conflict) || conflict.Current.VersionID != second.VersionID || len(conflict.Fields) == 0 || update.Content.Title != "Unsaved input" {
		t.Fatal("stale edit did not preserve current and submitted input", err)
	}
	for i, title := range []string{"Literal 100% dinner_", "Literal 100X dinnerY", "Épices"} {
		_, err := s.SavePersonalRecipe(ctx, actor, premium, PersonalSave{RequestKey: fmt.Sprintf("%032x", i+1), Content: personalDocument(title)}, now.Add(time.Duration(i+2)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.SearchPersonalRecipes(ctx, actor, "100% dinner_", 12, "", now)
	if err != nil || len(page.Recipes) != 1 || page.Recipes[0].Content.Title != "Literal 100% dinner_" {
		t.Fatal("search treated literal text as a pattern", err)
	}
	page, err = s.SearchPersonalRecipes(ctx, actor, "ÉPICES", 12, "", now)
	if err != nil || len(page.Recipes) != 1 {
		t.Fatal("Unicode title search failed", err)
	}
	page, err = s.SearchPersonalRecipes(ctx, actor, "", 2, "", now)
	if err != nil || len(page.Recipes) != 2 || page.NextCursor == nil {
		t.Fatal("first page unavailable", err)
	}
	next, err := s.SearchPersonalRecipes(ctx, actor, "", 2, *page.NextCursor, now)
	if err != nil || len(next.Recipes) != 2 || next.Recipes[0].ID == page.Recipes[0].ID {
		t.Fatal("keyset pagination duplicated a page", err)
	}
	reopened, err := Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.ReadPersonalRecipe(ctx, actor, first.ID, "", now)
	if err != nil || got.VersionID != second.VersionID {
		t.Fatal("saved current pointer did not survive restart", err)
	}
	input.Content = personalDocument("Private 100% dinner_")
	replay, err = reopened.SavePersonalRecipe(ctx, actor, premium, input, now)
	if err != nil || replay.VersionID != first.VersionID || replay.CurrentVersionID != second.VersionID {
		t.Fatal("restarted replay lost the original outcome or current pointer", err)
	}
}

func TestPersonalStorePermissionsCopyDeletionAndStaleReplay(t *testing.T) {
	s, actor, other, premium, now := personalFixture(t)
	ctx := t.Context()
	in := PersonalSave{RequestKey: strings.Repeat("d", 32), Content: personalDocument("Owned")}
	if _, err := s.SavePersonalRecipe(ctx, actor, nil, in, now); !errors.Is(err, ErrPersonalPermission) {
		t.Fatal("absent permission granted creation", err)
	}
	expired := func(context.Context, *gorm.DB, int64, string) (PersonalEntitlement, error) {
		return PersonalEntitlement{Premium: true, ExpiresUnix: now.Unix()}, nil
	}
	if _, err := s.SavePersonalRecipe(ctx, actor, expired, in, now); !errors.Is(err, ErrPersonalPermission) {
		t.Fatal("expired permission granted creation", err)
	}
	meal := shoppingMeal(t, s, actor.UserID, "2026-11-02", "Original", "0.25")
	eligibleImage := "https://res.cloudinary.com/fixture/image/upload/v1/mychef/copied.jpg"
	if err := s.DB.Model(&Meal{}).Where("meal_id=?", meal.ID).Update("recipeimg", eligibleImage).Error; err != nil {
		t.Fatal(err)
	}
	original := meal.Recipe()
	copy, err := s.CopyPersonalRecipe(ctx, actor, premium, meal.ID, now)
	if err != nil || copy.Draft.Content.Title != original.Title || !copy.Draft.NeedsServings {
		t.Fatal("source draft unavailable", err)
	}
	if copy.ImageURL == nil || *copy.ImageURL != eligibleImage {
		t.Fatal("eligible source photo omitted from copy preview")
	}
	if copy.Draft.SourceYield == nil || copy.Draft.Content.Instructions == nil || copy.Draft.Content.Tips == nil || copy.Draft.Content.Allergens == nil {
		t.Fatal("copy draft omitted empty array fields")
	}
	if _, err := s.CopyPersonalRecipe(ctx, other, premium, meal.ID, now); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatal("foreign copy accepted", err)
	}
	in.SourceRecipeID = &meal.ID
	first, err := s.SavePersonalRecipe(ctx, actor, premium, in, now)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestKey = strings.Repeat("e", 32)
	_, err = s.SavePersonalRecipe(ctx, actor, premium, in, now)
	var exists *PersonalCopyExists
	if !errors.As(err, &exists) || exists.ID != first.ID {
		t.Fatal("repeated copy silently duplicated source", err)
	}
	if _, err := s.ReadPersonalRecipe(ctx, actor, first.ID, "", now); err != nil {
		t.Fatal("retained read depended on Premium", err)
	}
	if err := s.DeletePersonalRecipe(ctx, other, first.ID, first.VersionID, strings.Repeat("f", 32), now); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatal("foreign deletion accepted", err)
	}
	for range 2 {
		if err := s.DeletePersonalRecipe(ctx, actor, first.ID, first.VersionID, strings.Repeat("f", 32), now); err != nil {
			t.Fatal("deletion or its replay failed", err)
		}
	}
	in.RequestKey = strings.Repeat("d", 32)
	if _, err := s.SavePersonalRecipe(ctx, actor, premium, in, now); !errors.Is(err, ErrPersonalDeleted) {
		t.Fatal("old save resurrected deleted source", err)
	}
	var versions int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipe_versions WHERE recipe_id=?", first.ID).Scan(&versions); err != nil || versions != 0 {
		t.Fatal("deleted private versions remained", err)
	}
	var originalTitle string
	if err := s.SQL.QueryRow("SELECT recipetitle FROM meals WHERE meal_id=?", meal.ID).Scan(&originalTitle); err != nil || originalTitle != original.Title {
		t.Fatal("library deletion rewrote original meal", err)
	}
	if err := s.DB.Model(&User{}).Where("user_id=?", actor.UserID).Update("email_verified_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.SearchPersonalRecipes(ctx, actor, "", 12, "", now); !errors.Is(err, ErrPersonalSession) {
		t.Fatal("unverified library access accepted", err)
	}
}

func TestPersonalStoreAtomicFailuresAndConcurrentVersions(t *testing.T) {
	s, actor, _, premium, now := personalFixture(t)
	ctx := t.Context()
	failure := errors.New("injected storage failure")
	for _, stage := range []string{"root", "version", "current", "receipt"} {
		input := PersonalSave{RequestKey: strings.Repeat("1", 32), Content: personalDocument("Fault fixture")}
		_, err := s.SavePersonalRecipe(ctx, actor, premium, input, now, PersonalWriteOptions{Fault: func(at string) error {
			if at == stage {
				return failure
			}
			return nil
		}})
		if !errors.Is(err, failure) {
			t.Fatal("fault not reached", stage, err)
		}
		for _, table := range []string{"personal_recipes", "personal_recipe_versions", "personal_recipe_operations"} {
			var count int
			if err := s.SQL.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
				t.Fatal("partial mutation after fault", stage, table, err)
			}
		}
	}
	first, err := s.SavePersonalRecipe(ctx, actor, premium, PersonalSave{RequestKey: strings.Repeat("2", 32), Content: personalDocument("Original")}, now)
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = secondStore.Close() }()
	var wg sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for i, db := range []*Store{s, secondStore} {
		wg.Go(func() {
			_, err := db.SavePersonalRecipe(ctx, actor, premium, PersonalSave{ID: first.ID, ExpectedVersion: first.VersionID, RequestKey: fmt.Sprintf("%032x", i+3), Content: personalDocument(fmt.Sprintf("Edit %d", i))}, now.Add(time.Second))
			errorsSeen <- err
		})
	}
	wg.Wait()
	close(errorsSeen)
	winners, conflicts := 0, 0
	for err := range errorsSeen {
		var conflict *PersonalVersionConflict
		if err == nil {
			winners++
		} else if errors.As(err, &conflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipe_versions WHERE recipe_id=?", first.ID).Scan(&count); err != nil || count != 2 || winners != 1 || conflicts != 1 {
		t.Fatal("concurrent edits lost or duplicated a version", count, winners, conflicts, err)
	}
}

func TestPersonalStoreRejectsUnsafeSearchAndRevokedSessions(t *testing.T) {
	s, actor, other, premium, now := personalFixture(t)
	for _, query := range []string{"bad\u0085query", string([]byte{0xff}), strings.Repeat("é", 201)} {
		if _, err := s.SearchPersonalRecipes(t.Context(), actor, query, 12, "", now); !errors.Is(err, ErrPersonalInput) {
			t.Fatal("unsafe search accepted", err)
		}
	}
	for _, cursor := range []string{"not a cursor", strings.Repeat("a", 129), "MDoz"} {
		if _, err := s.SearchPersonalRecipes(t.Context(), actor, "", 12, cursor, now); !errors.Is(err, ErrPersonalInput) {
			t.Fatal("invalid cursor accepted", err)
		}
	}
	in := PersonalSave{RequestKey: strings.Repeat("9", 32), Content: personalDocument("Owned")}
	recipe, err := s.SavePersonalRecipe(t.Context(), actor, premium, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavePersonalRecipe(t.Context(), other, premium, PersonalSave{ID: recipe.ID, ExpectedVersion: recipe.VersionID, RequestKey: strings.Repeat("8", 32), Content: personalDocument("Foreign edit")}, now); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatal("foreign write accepted", err)
	}
	if err := s.DB.Where("hash=?", actor.SessionHash).Delete(&Session{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadPersonalRecipe(t.Context(), actor, recipe.ID, "", now); !errors.Is(err, ErrPersonalSession) {
		t.Fatal("revoked read accepted", err)
	}
	if _, err := s.SavePersonalRecipe(t.Context(), actor, premium, in, now); !errors.Is(err, ErrPersonalSession) {
		t.Fatal("revoked replay accepted", err)
	}
}

func TestPersonalStoreUpdateAndDeleteFaultsPreserveCommittedVersion(t *testing.T) {
	s, actor, _, premium, now := personalFixture(t)
	failure := errors.New("injected mutation failure")
	first, err := s.SavePersonalRecipe(t.Context(), actor, premium, PersonalSave{RequestKey: strings.Repeat("5", 32), Content: personalDocument("Original")}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"root", "version", "current", "receipt"} {
		_, err := s.SavePersonalRecipe(t.Context(), actor, premium, PersonalSave{ID: first.ID, ExpectedVersion: first.VersionID, RequestKey: strings.Repeat("6", 32), Content: personalDocument("Unsaved")}, now, PersonalWriteOptions{Fault: func(at string) error {
			if at == stage {
				return failure
			}
			return nil
		}})
		if !errors.Is(err, failure) {
			t.Fatal("update fault missing", stage, err)
		}
		current, err := s.ReadPersonalRecipe(t.Context(), actor, first.ID, "", now)
		if err != nil || current.VersionID != first.VersionID || current.Content.Title != "Original" {
			t.Fatal("partial update survived", stage, err)
		}
	}
	for _, stage := range []string{"delete", "receipt"} {
		err := s.DeletePersonalRecipe(t.Context(), actor, first.ID, first.VersionID, strings.Repeat("7", 32), now, PersonalWriteOptions{Fault: func(at string) error {
			if at == stage {
				return failure
			}
			return nil
		}})
		if !errors.Is(err, failure) {
			t.Fatal("delete fault missing", stage, err)
		}
		current, err := s.ReadPersonalRecipe(t.Context(), actor, first.ID, "", now)
		if err != nil || current.VersionID != first.VersionID {
			t.Fatal("delete fault lost version", stage, err)
		}
	}
	var versions, receipts int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipe_versions").Scan(&versions); err != nil || versions != 1 {
		t.Fatal("orphaned update version", versions, err)
	}
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipe_operations").Scan(&receipts); err != nil || receipts != 1 {
		t.Fatal("orphaned mutation receipt", receipts, err)
	}
	if err := s.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
}
