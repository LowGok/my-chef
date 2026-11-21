package menus

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func generationTier(now time.Time, premium *atomic.Bool) *store.PlanningPolicy {
	return &store.PlanningPolicy{Now: func() time.Time { return now },
		HouseholdZone: func(context.Context, *gorm.DB, int64) (string, error) { return "Europe/Brussels", nil },
		Entitlements: func(context.Context, *gorm.DB, int64, string) (store.PersonalEntitlement, error) {
			return store.PersonalEntitlement{Premium: premium.Load()}, nil
		}}
}

func TestTierGenerationPreservesExistingDinnersOnUpgradeAndRefresh(t *testing.T) {
	s, user := workerStore(t)
	ctx := t.Context()
	var premium atomic.Bool
	policy := generationTier(time.Date(2026, 11, 19, 16, 0, 0, 0, time.UTC), &premium)
	service, err := New(s, providers.Fixture{}, nil, Options{Planning: policy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	base := request("Belgium")
	base.Dates = []string{"2026-11-20"}
	if _, err := service.Start(ctx, user, "free-friday", base); !errors.Is(err, store.ErrPlanningTier) {
		t.Fatal("Free could create a fourth slot", err)
	}
	base.Dates = []string{"2026-11-16", "2026-11-17", "2026-11-18"}
	run := func(key string, in domain.MenuInput) {
		t.Helper()
		op, err := service.Start(ctx, user, key, in)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := strconv.ParseInt(op.ID, 10, 64)
		if result := waitOperation(t, service, user, id); result.Status != "succeeded" {
			t.Fatal(result)
		}
	}
	list := func() []domain.Recipe {
		t.Helper()
		p, err := s.History(ctx, user, "2026-11-16", "2026-11-23", 20, 0)
		if err != nil {
			t.Fatal(err)
		}
		return p.Recipes
	}
	run("free-three-meals", base)
	first := list()
	if len(first) != 3 {
		t.Fatal("late signup lost its fixed slots", first)
	}
	premium.Store(true)
	appendSlots := base
	appendSlots.Dates = []string{"2026-11-19", "2026-11-20", "2026-11-21", "2026-11-22"}
	run("append-four-dinners", appendSlots)
	full := list()
	if len(full) != 7 {
		t.Fatal("upgrade did not extend the plan", len(full))
	}
	for i := range first {
		if full[i].ID != first[i].ID {
			t.Fatal("upgrade replaced original dinner", first[i].Date)
		}
	}
	refresh := base
	refresh.Dates = []string{"2026-11-17"}
	refresh.ReplaceExisting = true
	run("replace-one-dinner", refresh)
	updated := list()
	if len(updated) != 7 || updated[1].ID == first[1].ID {
		t.Fatal("selected slot was not replaced")
	}
	for i := range full {
		if i != 1 && updated[i].ID != full[i].ID {
			t.Fatal("refresh removed unrelated dinner", full[i].Date)
		}
	}
	premium.Store(false)
	run("free-one-replacement", refresh)
	if _, err := service.Start(ctx, user, "free-second-replacement", refresh); !errors.Is(err, store.ErrPlanningTier) {
		t.Fatal("second ordinary Free replacement was admitted", err)
	}
}

type blockedTierProvider struct{ entered, release chan struct{} }

func (p blockedTierProvider) Generate(ctx context.Context, in domain.GenerationRequest) ([]domain.RecipeContent, error) {
	close(p.entered)
	select {
	case <-p.release:
		return (providers.Fixture{}).Generate(ctx, in)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type oneFailedReplacement struct{ calls atomic.Int32 }

func (p *oneFailedReplacement) Generate(ctx context.Context, in domain.GenerationRequest) ([]domain.RecipeContent, error) {
	if p.calls.Add(1) == 1 {
		return nil, providers.Failure("provider_quota")
	}
	return (providers.Fixture{}).Generate(ctx, in)
}

func TestTierReplacementFailureReleasesButOverlappingTimezoneKeepsAllowanceSpent(t *testing.T) {
	s, user := workerStore(t)
	ctx := t.Context()
	var premium atomic.Bool
	var zone atomic.Value
	zone.Store("Etc/GMT+12")
	var timeNS atomic.Int64
	now := time.Date(2026, 11, 15, 16, 0, 0, 0, time.UTC)
	timeNS.Store(now.UnixNano())
	policy := generationTier(now, &premium)
	policy.Now = func() time.Time { return time.Unix(0, timeNS.Load()) }
	policy.HouseholdZone = func(context.Context, *gorm.DB, int64) (string, error) { return zone.Load().(string), nil }
	for _, day := range []string{"2026-11-11", "2026-11-16", "2026-11-23"} {
		m := store.Meal{CreatorID: user, CreationDate: day, RecipeTitle: "Existing dinner " + day, ImageStatus: "ready"}
		if err := s.DB.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := &oneFailedReplacement{}
	service, err := New(s, p, nil, Options{Planning: policy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	in := request("Belgium")
	in.Dates = []string{"2026-11-11"}
	in.ReplaceExisting = true
	run := func(key, status string) {
		t.Helper()
		op, err := service.Start(ctx, user, key, in)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := strconv.ParseInt(op.ID, 10, 64)
		if out := waitOperation(t, service, user, id); out.Status != status {
			t.Fatal(out)
		}
	}
	run("allowance-failure", "failed")
	run("replacement-after-failure", "succeeded")
	zone.Store("Pacific/Kiritimati")
	in.Dates = []string{"2026-11-16"}
	if _, err := service.Start(ctx, user, "lapping-monday", in); !errors.Is(err, store.ErrPlanningTier) {
		t.Fatal("changing time zones created a second allowance during an overlapping week", err)
	}
	if p.calls.Load() != 2 {
		t.Fatal("rejected replacement got to the provider")
	}
	timeNS.Store(now.AddDate(0, 0, 7).UnixNano())
	in.Dates = []string{"2026-11-23"}
	run("next-nonoverlapping-week", "succeeded")
}

func TestTierGenerationRechecksEntitlementAfterProviderReturns(t *testing.T) {
	s, user := workerStore(t)
	ctx := t.Context()
	var premium atomic.Bool
	premium.Store(true)
	provider := blockedTierProvider{make(chan struct{}), make(chan struct{})}
	service, err := New(s, provider, nil, Options{Planning: generationTier(time.Date(2026, 11, 19, 16, 0, 0, 0, time.UTC), &premium)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	in := request("Belgium")
	in.Dates = []string{"2026-11-21"}
	op, err := service.Start(ctx, user, "premium-pending", in)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture provider did not start")
	}
	premium.Store(false)
	close(provider.release)
	id, _ := strconv.ParseInt(op.ID, 10, 64)
	saved := waitOperation(t, service, user, id)
	if saved.Status != "failed" || saved.ErrorCode == nil || *saved.ErrorCode != "planning_limit" {
		t.Fatal("in-flight downgrade did not release the reservation", saved)
	}
	if p, err := s.History(ctx, user, "2026-11-16", "2026-11-23", 20, 0); err != nil || len(p.Recipes) != 0 {
		t.Fatal("revoked Premium still saved a dinner", p, err)
	}
	if replay, err := service.Start(ctx, user, "premium-pending", in); err != nil || replay.ID != op.ID || replay.Status != "failed" {
		t.Fatal("retry allocated a new reservation", replay, err)
	}
}

func TestTierReplacementAllowanceIsSharedByFavoriteAndGeneration(t *testing.T) {
	for _, order := range []string{"swap first", "generation first", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			s, user := workerStore(t)
			ctx := t.Context()
			var premium atomic.Bool
			policy := generationTier(time.Date(2026, 11, 19, 16, 0, 0, 0, time.UTC), &premium)
			service, err := New(s, providers.Fixture{}, nil, Options{Planning: policy})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(service.Close)
			two := []store.Meal{}
			for _, date := range []string{"2026-11-16", "2026-11-17"} {
				m := store.Meal{CreatorID: user, CreationDate: date, RecipeTitle: "Dinner " + date, ImageStatus: "ready"}
				if err := s.DB.Create(&m).Error; err != nil {
					t.Fatal(err)
				}
				two = append(two, m)
			}
			if _, err := s.SetFavorite(ctx, user, two[0].ID, true, store.PersonalPlanning{Tier: policy}); err != nil {
				t.Fatal(err)
			}
			swap := domain.FavoriteSwap{Week: "2026-11-16", Today: "2026-11-19", RequestKey: "test-favorite-replacement", TargetID: strconv.FormatInt(two[1].ID, 10)}
			swapMeal := func() (domain.Recipe, error) {
				return s.SwapFavorite(ctx, user, two[0].ID, swap, store.PersonalPlanning{Tier: policy})
			}
			in := request("Belgium")
			in.Dates = []string{"2026-11-16"}
			in.ReplaceExisting = true
			generate := func() (domain.Operation, error) { return service.Start(ctx, user, "test-generated-replacement", in) }
			wait := func(op domain.Operation) {
				t.Helper()
				id, _ := strconv.ParseInt(op.ID, 10, 64)
				if completed := waitOperation(t, service, user, id); completed.Status != "succeeded" {
					t.Fatal(completed)
				}
			}
			switch order {
			case "swap first":
				one, err := swapMeal()
				if err != nil {
					t.Fatal(err)
				}
				retry, err := swapMeal()
				if err != nil || retry.ID != one.ID {
					t.Fatal("retry was charged twice", retry, err)
				}
				if _, err := generate(); !errors.Is(err, store.ErrPlanningTier) {
					t.Fatal("generation granted a second Free replacement", err)
				}
			case "generation first":
				op, err := generate()
				if err != nil {
					t.Fatal(err)
				}
				wait(op)
				if _, err := swapMeal(); !errors.Is(err, store.ErrPlanningTier) {
					t.Fatal("favorite granted a second Free replacement", err)
				}
			default:
				start := make(chan struct{})
				swapResult := make(chan error, 1)
				type answer struct {
					op  domain.Operation
					err error
				}
				generateResult := make(chan answer, 1)
				go func() { <-start; _, err := swapMeal(); swapResult <- err }()
				go func() { <-start; op, err := generate(); generateResult <- answer{op, err} }()
				close(start)
				swapErr, generated := <-swapResult, <-generateResult
				if (swapErr == nil) == (generated.err == nil) {
					t.Fatal("concurrent routes must admit exactly one replacement", swapErr, generated.err)
				}
				if generated.err == nil {
					wait(generated.op)
				}
			}
		})
	}
}

func TestTierCorrectionUsesSavedEvidenceWithoutSpendingReplacement(t *testing.T) {
	s, user := workerStore(t)
	ctx := t.Context()
	var premium atomic.Bool
	premium.Store(true)
	policy := generationTier(time.Date(2026, 11, 19, 16, 0, 0, 0, time.UTC), &premium)
	service, err := New(s, providers.Fixture{}, nil, Options{Planning: policy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	settle := func(op domain.Operation, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		id, _ := strconv.ParseInt(op.ID, 10, 64)
		if result := waitOperation(t, service, user, id); result.Status != "succeeded" {
			t.Fatal(result.Status, result.ErrorCode)
		}
	}
	plan := request("Belgium")
	plan.Dates = []string{"2026-11-16", "2026-11-17", "2026-11-20"}
	settle(service.Start(ctx, user, "correction-fixture-plan", plan))
	premium.Store(false)
	list := func() []domain.Recipe {
		t.Helper()
		page, err := s.History(ctx, user, "2026-11-16", "2026-11-23", 20, 0)
		if err != nil || len(page.Recipes) != 3 {
			t.Fatal(page, err)
		}
		return page.Recipes
	}
	dinner := func(recipe domain.Recipe) int64 {
		id, _ := strconv.ParseInt(recipe.ID, 10, 64)
		return id
	}
	first := list()
	if _, err := service.StartCorrection(ctx, user, "healthy-no-exemption", dinner(first[1])); !errors.Is(err, store.ErrPlanningCorrection) {
		t.Fatal("unsubstantiated correction was not denied", err)
	}
	ordinary := plan
	ordinary.Dates, ordinary.ReplaceExisting = []string{"2026-11-16"}, true
	settle(service.Start(ctx, user, "spend-free-replacement", ordinary))
	before := list()
	if err := s.DB.Model(&store.User{}).Where("user_id=?", user).Update("dislikes", "Leek").Error; err != nil {
		t.Fatal(err)
	}
	op, err := service.StartCorrection(ctx, user, "correct-saved-exclusion", dinner(before[1]))
	settle(op, err)
	replay, err := service.StartCorrection(ctx, user, "correct-saved-exclusion", dinner(before[1]))
	if err != nil || replay.ID != op.ID || replay.Status != "succeeded" {
		t.Fatal("correction replay did not recover its original operation", replay, err)
	}
	if _, err := service.StartCorrection(ctx, user, "correct-saved-exclusion", dinner(before[2])); err == nil {
		t.Fatal("correction key admitted a different meal")
	}
	if err := s.DB.Model(&store.Meal{}).Where("meal_id=?", dinner(before[2])).Update("instructions", "[]").Error; err != nil {
		t.Fatal(err)
	}
	settle(service.StartCorrection(ctx, user, "correct-retained-friday", dinner(before[2])))
	after := list()
	if after[0].ID != before[0].ID || after[1].ID == before[1].ID || after[2].ID == before[2].ID || after[1].Ingredients[0].Name != "Carrot" {
		t.Fatal("correction did not replace only the verified dinner", after)
	}
	if _, err := service.Start(ctx, user, "still-no-extra-replacement", ordinary); !errors.Is(err, store.ErrPlanningTier) {
		t.Fatal("correction reopened the ordinary allowance", err)
	}
}

func TestTierCorrectionRechecksExclusionAfterProviderReturns(t *testing.T) {
	s, user := workerStore(t)
	ctx := t.Context()
	var premium atomic.Bool
	policy := generationTier(time.Date(2026, 11, 19, 16, 0, 0, 0, time.UTC), &premium)
	blocked := blockedTierProvider{entered: make(chan struct{}), release: make(chan struct{})}
	service, err := New(s, blocked, nil, Options{Planning: policy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	meal := store.Meal{CreatorID: user, CreationDate: "2026-11-16", RecipeTitle: "Leek soup", Instructions: store.Ptr(`["Cook the soup."]`), ImageStatus: "ready"}
	if err := s.DB.Create(&meal).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&store.Ingredient{RecipeID: meal.ID, Name: "Leek"}).Error; err != nil {
		t.Fatal(err)
	}
	prefs := s.DB.Model(&store.User{}).Where("user_id=?", user)
	if err := prefs.Update("dislikes", "Leek").Error; err != nil {
		t.Fatal(err)
	}
	op, err := service.StartCorrection(ctx, user, "correction-with-changing-exclusions", meal.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never started")
	}
	if err := prefs.Update("dislikes", nil).Error; err != nil {
		t.Fatal(err)
	}
	close(blocked.release)
	id, _ := strconv.ParseInt(op.ID, 10, 64)
	result := waitOperation(t, service, user, id)
	if result.Status != "failed" || result.ErrorCode == nil || *result.ErrorCode != "correction_unavailable" {
		t.Fatal("correction settled after its proof was withdrawn", result)
	}
	var retained store.Meal
	if err := s.DB.First(&retained, "meal_id=?", meal.ID).Error; err != nil || retained.RecipeTitle != meal.RecipeTitle {
		t.Fatal("the original dinner was changed", err)
	}
}
