package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestTierPlanningReadsUseTheHouseholdWeekForEveryDirectID(t *testing.T) {
	s, _, actor := accountDeletionFixture(t)
	ctx := t.Context()
	// Sunday in UTC has already become Monday in the confirmed timezone.
	now := time.Date(2026, 11, 15, 10, 30, 0, 0, time.UTC)
	premium := false
	zone := "Pacific/Kiritimati"
	policy := &PlanningPolicy{Now: func() time.Time { return now },
		HouseholdZone: func(context.Context, *gorm.DB, int64) (string, error) { return zone, nil },
		Entitlements: func(context.Context, *gorm.DB, int64, string) (PersonalEntitlement, error) {
			return PersonalEntitlement{Premium: premium}, nil
		}}
	menu := MenuRecord{UserID: actor.UserID, CreatedUnix: now.Unix()}
	if err := s.DB.Create(&menu).Error; err != nil {
		t.Fatal(err)
	}
	makeMeal := func(day, title string) Meal {
		t.Helper()
		m := Meal{MenuID: &menu.ID, CreatorID: actor.UserID, CreationDate: day, RecipeTitle: title, ImageStatus: "ready"}
		if err := s.DB.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
		return m
	}
	past := makeMeal("2026-11-15", "Past private meal")
	current := makeMeal("2026-11-22", "Retained Sunday meal")
	future := makeMeal("2026-11-23", "Next week meal")
	fourth := makeMeal("2026-12-20", "Fourth future week meal")
	fifth := makeMeal("2026-12-21", "Fifth future week meal")
	for _, m := range []Meal{past, future, fourth, fifth} {
		if _, err := s.GetRecipe(ctx, actor.UserID, m.ID, policy); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatal("Free direct recipe allowed outside local week", m.CreationDate, err)
		}
	}
	if _, err := s.GetRecipe(ctx, actor.UserID, current.ID, policy); err != nil {
		t.Fatal("downgrade hid a retained current-week dinner", err)
	}
	if _, err := s.SetFavorite(ctx, actor.UserID, fifth.ID, true, PersonalPlanning{Tier: policy}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("saving by direct ID exposed a protected meal", err)
	}
	if _, err := s.SetFavorite(ctx, actor.UserID, past.ID, true); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetRecipe(ctx, actor.UserID, past.ID, policy)
	if err != nil || !saved.IsFavorite || saved.MenuID != nil {
		t.Fatal("separate saved favorite was not available independently of its plan", saved, err)
	}
	result, err := s.GetMenu(ctx, actor.UserID, menu.ID, policy)
	if err != nil || len(result.Recipes) != 1 || result.Recipes[0].Title != current.RecipeTitle {
		t.Fatal("direct menu bypassed week filtering", result, err)
	}
	page, err := s.History(ctx, actor.UserID, "2026-11-15", "2026-12-22", 1, 0, policy)
	if err != nil || len(page.Recipes) != 1 || page.Recipes[0].Title != current.RecipeTitle || page.NextOffset != nil {
		t.Fatal("list paginated hidden meals", page, err)
	}
	premium = true
	if _, err := s.GetRecipe(ctx, actor.UserID, fourth.ID, policy); err != nil {
		t.Fatal("Premium lost its fourth future week", err)
	}
	if _, err := s.GetRecipe(ctx, actor.UserID, fifth.ID, policy); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("Premium read beyond its horizon", err)
	}
	premium = false
	now = now.AddDate(0, 0, 7)
	if _, err := s.GetRecipe(ctx, actor.UserID, future.ID, policy); err != nil {
		t.Fatal("Free did not regain the retained meal when its week arrived", err)
	}
	zone = "Local"
	if _, err := s.GetRecipe(ctx, actor.UserID, future.ID, policy); !errors.Is(err, ErrPlanningTimezone) {
		t.Fatal("uncorroborated device timezone silently became authoritative", err)
	}
}
