package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"gorm.io/gorm"
)

var ErrPlanningTier = errors.New("this request exceeds the plan's dinner or replacement allowance")

// PlanningAllocation is private operation metadata. It is stored alongside
// the captured recipe request, never sent to the recipe provider or client.
type PlanningAllocation struct {
	CorrectionMealID int64          `json:"correctionMealId,omitempty"`
	Slots            []PlanningSlot `json:"slots"`
	Weeks            []PlanningWeek `json:"weeks"`
	FreeReplacement  bool           `json:"freeReplacement"`
	Today            string         `json:"today"`
}

type PlanningSlot struct {
	Date   string `json:"date"`
	MealID int64  `json:"mealId"`
}

type PlanningWeek struct {
	Key      string `json:"key"`
	StartsNS int64  `json:"startsNs"`
	EndsNS   int64  `json:"endsNs"`
}

type GenerationSnapshot struct {
	domain.GenerationRequest
	Allocation *PlanningAllocation `json:"allocation,omitempty"`
}

func allocationWeek(monday string, zone *time.Location) PlanningWeek {
	day, _ := time.ParseInLocation(time.DateOnly, monday, zone)
	return PlanningWeek{Key: monday, StartsNS: day.UnixNano(), EndsNS: day.AddDate(0, 0, 7).UnixNano()}
}

func checkFreeReplacement(tx *gorm.DB, user, operation int64, weeks []PlanningWeek) error {
	for _, w := range weeks {
		var used bool
		// The operation status settles/releases the reservation, using the
		// original overlap window even when the household changes timezone.
		if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM generations g, json_each(g.request_json,'$.allocation.weeks') w
 WHERE g.user_id=? AND g.id<>? AND g.status IN ('queued','running','succeeded')
 AND json_extract(g.request_json,'$.allocation.freeReplacement')=1
 AND json_extract(w.value,'$.startsNs')<? AND json_extract(w.value,'$.endsNs')>?
 UNION ALL
 SELECT 1 FROM favorite_swaps f, json_each(f.body,'$.allocation.weeks') w
 WHERE f.user_id=? AND json_extract(f.body,'$.allocation.freeReplacement')=1
 AND json_extract(w.value,'$.startsNs')<? AND json_extract(w.value,'$.endsNs')>?)`,
			user, operation, w.EndsNS, w.StartsNS, user, w.EndsNS, w.StartsNS).Row().Scan(&used); err != nil {
			return err
		}
		if used {
			return ErrPlanningTier
		}
	}
	return nil
}

func (p *PlanningPolicy) reserveFavoriteReplacement(ctx context.Context, tx *gorm.DB, user int64, meal Meal) (*PlanningAllocation, error) {
	if p == nil {
		return nil, nil
	}
	if _, err := time.Parse(time.DateOnly, meal.CreationDate); err != nil {
		return nil, ErrPlanningTier
	}
	allocation, err := p.generationAllocation(ctx, tx, user, 0, domain.MenuInput{Dates: []string{meal.CreationDate}, ReplaceExisting: true}, nil)
	if err != nil {
		return nil, err
	}
	if len(allocation.Slots) != 1 || allocation.Slots[0].MealID != meal.ID {
		return nil, ErrFavoriteConflict
	}
	return allocation, nil
}

// ReserveGeneration captures exact dinner targets in the same write
// transaction as the queued operation. Status controls when the reservation
// becomes settled or released. No future action may reinterpret its dates.
func (p *PlanningPolicy) ReserveGeneration(ctx context.Context, tx *gorm.DB, user int64, input domain.MenuInput) (*PlanningAllocation, error) {
	if p == nil {
		return nil, CheckMenuReplacement(tx, user, input)
	}
	return p.generationAllocation(ctx, tx, user, 0, input, nil)
}

var ErrPlanningCorrection = errors.New("the saved dinner has no confirmed issue to correct")

// ReserveCorrection derives both the request and its exemption from a saved
// dinner. A caller can name a dinner, but cannot attest to a recipe fault.
func (p *PlanningPolicy) ReserveCorrection(ctx context.Context, tx *gorm.DB, owner User, mealID int64) (domain.MenuInput, *PlanningAllocation, error) {
	if p == nil {
		return domain.MenuInput{}, nil, ErrPlanningTier
	}
	meal, w, err := p.checkCorrection(ctx, tx, owner, mealID)
	if err != nil {
		return domain.MenuInput{}, nil, err
	}

	// Keep the original household settings when the dinner came from the
	// planner. Imported legacy dinners have no generation settings.
	input := domain.MenuInput{Adults: 2, MaxMinutes: 45, Budget: "medium", Region: "Belgium", Creativity: 5}
	if meal.MenuID != nil {
		var prior Generation
		err := tx.Where("user_id=? AND menu_id=? AND status=?", owner.ID, *meal.MenuID, "succeeded").First(&prior).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.MenuInput{}, nil, err
		}
		if err == nil {
			var saved GenerationSnapshot
			if json.Unmarshal([]byte(prior.RequestJSON), &saved) == nil && saved.Validate() == nil {
				input = saved.MenuInput
			}
		}
	}
	input.Dates, input.ReplaceExisting, input.FavoriteRevisions = []string{meal.CreationDate}, true, nil
	allocation := &PlanningAllocation{
		CorrectionMealID: mealID,
		Slots:            []PlanningSlot{{Date: meal.CreationDate, MealID: mealID}},
		Weeks:            []PlanningWeek{allocationWeek(input.WeekStarts()[0], w.First.Location())},
		Today:            w.Today,
	}
	return input, allocation, nil
}

func (p *PlanningPolicy) checkCorrection(ctx context.Context, tx *gorm.DB, owner User, mealID int64) (Meal, PlanningWindow, error) {
	var meal Meal
	w, err := p.Window(ctx, tx, owner.ID)
	if err != nil {
		return meal, w, err
	}
	if err := w.meals(tx).Preload("Ingredients").Where("creator_id=? AND meal_id=?", owner.ID, mealID).First(&meal).Error; err != nil {
		return meal, w, err
	}
	day, err := time.Parse(time.DateOnly, meal.CreationDate)
	if err != nil || day.Format(time.DateOnly) != meal.CreationDate {
		return meal, w, ErrPlanningTier
	}
	var count int64
	if err := tx.Model(&Meal{}).Where("creator_id=? AND creationdate=?", owner.ID, meal.CreationDate).Count(&count).Error; err != nil {
		return meal, w, err
	}
	if count != 1 {
		return meal, w, domain.ErrMenuExists
	}
	display := meal.Recipe()
	hasInstructions := slices.ContainsFunc(display.Instructions, func(s string) bool { return strings.TrimSpace(s) != "" })
	hasIngredient := slices.ContainsFunc(meal.Ingredients, func(i Ingredient) bool { return strings.TrimSpace(i.Name) != "" })
	if strings.TrimSpace(display.Title) == "" || !hasInstructions || !hasIngredient {
		return meal, w, nil
	}
	content := domain.RecipeContent{Allergens: display.Allergens, IsMeatOrFish: meal.IsMeatOrFish != nil && *meal.IsMeatOrFish}
	for _, ingredient := range meal.Ingredients {
		content.Ingredients = append(content.Ingredients, domain.IngredientContent{Name: ingredient.Name})
	}
	if content.CheckExclusions(owner.Preferences()) != nil {
		return meal, w, nil
	}
	return meal, w, ErrPlanningCorrection
}

func (p *PlanningPolicy) generationAllocation(ctx context.Context, tx *gorm.DB, user, operation int64, input domain.MenuInput, original *PlanningAllocation) (*PlanningAllocation, error) {
	w, err := p.Window(ctx, tx, user)
	if err != nil {
		return nil, err
	}
	for _, date := range input.Dates {
		if !w.Includes(date) || !w.Premium && date >= w.First.AddDate(0, 0, 3).Format(time.DateOnly) {
			return nil, ErrPlanningTier
		}
	}
	if input.ReplaceExisting && !w.Premium && len(input.Dates) != 1 {
		return nil, ErrPlanningTier
	}
	out := &PlanningAllocation{FreeReplacement: input.ReplaceExisting && !w.Premium, Today: w.Today}
	for _, monday := range input.WeekStarts() {
		out.Weeks = append(out.Weeks, allocationWeek(monday, w.First.Location()))
	}
	for _, date := range input.Dates {
		var found []int64
		if err := tx.Model(&Meal{}).Where("creator_id=? AND creationdate=?", user, date).Limit(2).Pluck("meal_id", &found).Error; err != nil {
			return nil, err
		}
		if len(found) > 1 || input.ReplaceExisting && len(found) != 1 || !input.ReplaceExisting && len(found) != 0 {
			return nil, domain.ErrMenuExists
		}
		slot := PlanningSlot{Date: date}
		if len(found) == 1 {
			slot.MealID = found[0]
		}
		out.Slots = append(out.Slots, slot)
	}
	if original != nil {
		if !slices.Equal(original.Slots, out.Slots) || len(original.Weeks) == 0 {
			return nil, domain.ErrMenuExists
		}
		out.Weeks = original.Weeks
		out.Today = original.Today
		out.FreeReplacement = out.FreeReplacement || original.FreeReplacement
	}
	if out.FreeReplacement {
		if err := checkFreeReplacement(tx, user, operation, out.Weeks); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (a *PlanningAllocation) targets() []int64 {
	ids := make([]int64, 0, len(a.Slots))
	for _, slot := range a.Slots {
		if slot.MealID > 0 {
			ids = append(ids, slot.MealID)
		}
	}
	return ids
}
