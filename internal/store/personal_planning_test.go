package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"gorm.io/gorm"
)

func personalPlanningFixture(t *testing.T) (*Store, *LifecycleJournal, PersonalPlanning, PersonalRecipe, time.Time) {
	t.Helper()
	s, j, actor, _, reader, now := personalPhotoStore(t)
	in := PersonalSave{RequestKey: strings.Repeat("a", 32), Content: personalDocument("Private version one"), PhotoAction: "replace", PhotoDigest: personalUpload().Digest}
	r, err := j.ReservePersonalPhoto(t.Context(), actor, reader, in, personalUpload(), "192.0.2.1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.BeginPersonalPhoto(t.Context(), actor, reader, in, r, now); err != nil {
		t.Fatal(err)
	}
	recipe, err := j.FinishPersonalPhoto(t.Context(), actor, reader, in, r, true, now)
	if err != nil {
		t.Fatal(err)
	}
	return s, j, PersonalPlanning{Actor: actor, Entitlements: reader, Now: now}, recipe, now
}

func TestPersonalPlanningCapturesVersionAndSurvivesLibraryDeletion(t *testing.T) {
	s, j, permission, first, now := personalPlanningFixture(t)
	ctx := t.Context()
	week := "2026-11-09"
	queued, err := s.QueuePersonalRecipe(ctx, permission.Actor, permission.Entitlements, first.ID, first.VersionID, week, strings.Repeat("b", 32), now)
	if err != nil || len(queued.Recipes) != 1 || queued.Recipes[0].Source == nil || queued.Recipes[0].Source.Kind != "personal" || len(queued.Entries) != 1 {
		t.Fatal("personal queue not typed", err)
	}
	if queued.Recipes[0].Source.VersionID != first.VersionID || queued.Recipes[0].Source.PhotoID == nil {
		t.Fatal("queue lost version/photo")
	}
	second, err := s.SavePersonalRecipe(ctx, permission.Actor, permission.Entitlements, PersonalSave{ID: first.ID, ExpectedVersion: first.VersionID, RequestKey: strings.Repeat("c", 32), Content: personalDocument("Version two")}, now)
	if err != nil {
		t.Fatal(err)
	}
	stable, err := s.FavoriteWeek(ctx, permission.Actor.UserID, week, permission)
	if err != nil || stable.Recipes[0].Title != first.Content.Title || stable.Revision != queued.Revision {
		t.Fatal("library edit changed queued content/review", err)
	}
	input := domain.MenuInput{Dates: []string{week}, Adults: 2, MaxMinutes: 30, Budget: "low", Region: "BE", Creativity: 3, FavoriteRevisions: map[string]string{week: queued.Revision}}
	var captured []domain.PlannedFavorite
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		captured, err = CaptureFavorites(tx, permission.Actor.UserID, input, func([]string) {}, permission)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(domain.GenerationRequest{MenuInput: input, Favorites: captured})
	op := Generation{UserID: permission.Actor.UserID, Key: "captured-personal", RequestHash: "hash", RequestJSON: string(request), Status: "running", Stage: "saving"}
	if err := s.DB.Create(&op).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePersonalRecipe(ctx, permission.Actor, first.ID, second.VersionID, strings.Repeat("d", 32), now); err != nil {
		t.Fatal(err)
	}
	remaining, err := s.FavoriteWeek(ctx, permission.Actor.UserID, week, permission)
	if err != nil || len(remaining.Recipes) != 0 {
		t.Fatal("deleted source left pending entry", err)
	}
	cleaner := &personalTestCleaner{fakeAssetCleaner: fakeAssetCleaner{binding: "fixture"}}
	if _, err := j.CleanupStep(ctx, cleaner, now.Add(2*time.Minute)); err != nil || len(cleaner.private) != 0 {
		t.Fatal("captured operation photo deleted", err)
	}
	menu, err := s.SaveMenu(ctx, permission.Actor.UserID, op.ID, input, nil, permission.Entitlements)
	if err != nil || len(menu.Recipes) != 1 || menu.Recipes[0].Title != first.Content.Title {
		t.Fatal("captured operation changed after library deletion", err)
	}
	meal := menu.Recipes[0]
	if meal.Source == nil || meal.Source.RecipeID != first.ID || meal.Source.VersionID != first.VersionID || meal.Source.PhotoID == nil {
		t.Fatal("planned source relationship lost")
	}
	if _, err := s.ReadPersonalPhoto(ctx, permission.Actor, *first.PhotoID, now); err != nil {
		t.Fatal("retained planned photo unreadable", err)
	}
	id, _ := strconv.ParseInt(meal.ID, 10, 64)
	if _, err := s.SetFavorite(ctx, permission.Actor.UserID, id, true, permission); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatal("retained meal resurrected deleted source", err)
	}
	shopping, err := s.Shopping(ctx, permission.Actor.UserID, week)
	if err != nil || len(shopping.Items) != 1 || shopping.Items[0].Quantity == nil || *shopping.Items[0].Quantity != "12345678901234567890.00000000001" {
		t.Fatal("snapshot lost exact shopping quantity", err)
	}
	if _, err := j.CleanupStep(ctx, cleaner, now.Add(26*time.Hour)); err != nil || len(cleaner.private) != 0 {
		t.Fatal("planned photo cleaned while retained", err)
	}
	if err := s.DB.Where("creator_id=?", permission.Actor.UserID).Delete(&Meal{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Where("user_id=?", permission.Actor.UserID).Delete(&Generation{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := j.CleanupStep(ctx, cleaner, now.Add(52*time.Hour)); err != nil || len(cleaner.private) != 1 {
		t.Fatal("last retained reference did not release photo", err)
	}
}

func TestPersonalFavoritesExpiryDeletionAndBlockedRemoval(t *testing.T) {
	s, _, permission, first, now := personalPlanningFixture(t)
	ctx := t.Context()
	week := "2026-11-09"
	var meal Meal
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		snapshot, err := personalRecipeSnapshot(tx, permission.Actor, first)
		if err != nil {
			return err
		}
		meal, err = copyRecipe(tx, permission.Actor.UserID, nil, week, snapshot)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	favorite, err := s.SetFavorite(ctx, permission.Actor.UserID, meal.ID, true, permission)
	if err != nil || favorite.Source == nil || favorite.Source.VersionID != first.VersionID {
		t.Fatal("favorite lost personal provenance", err)
	}
	queued, err := s.QueueFavorite(ctx, permission.Actor.UserID, meal.ID, "2026-11-16", permission)
	if err != nil || len(queued.Entries) != 1 {
		t.Fatal("linked favorite not queued", err)
	}
	input := domain.MenuInput{Dates: []string{"2026-11-16"}, Adults: 2, MaxMinutes: 30, Budget: "low", Region: "BE", Creativity: 3, FavoriteRevisions: map[string]string{"2026-11-16": queued.Revision}}
	expired := permission
	expired.Entitlements = nil
	blocked, err := s.FavoriteWeek(ctx, permission.Actor.UserID, "2026-11-16", expired)
	if err != nil || len(blocked.Entries) != 1 || !blocked.Entries[0].Blocked || blocked.Revision == queued.Revision {
		t.Fatal("expired entry not explained in review", err)
	}
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		_, err := CaptureFavorites(tx, permission.Actor.UserID, input, func([]string) {}, expired)
		return err
	}); err == nil {
		t.Fatal("expired capture accepted")
	}
	input.FavoriteRevisions["2026-11-16"] = blocked.Revision
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		_, err := CaptureFavorites(tx, permission.Actor.UserID, input, func([]string) {}, expired)
		return err
	}); !errors.Is(err, ErrPersonalPermission) {
		t.Fatal("fresh review bypassed blocked personal entry", err)
	}
	if _, err := s.QueueFavorite(ctx, permission.Actor.UserID, meal.ID, "2026-11-16", expired); !errors.Is(err, ErrPersonalPermission) {
		t.Fatal("favorite bypassed Premium", err)
	}
	if _, err := s.QueuePersonalRecipe(ctx, permission.Actor, nil, first.ID, first.VersionID, "2026-11-16", strings.Repeat("b", 32), now); !errors.Is(err, ErrPersonalPermission) {
		t.Fatal("direct queue bypassed Premium", err)
	}
	target := shoppingMeal(t, s, permission.Actor.UserID, week, "Replace this", "1")
	swap := domain.FavoriteSwap{Week: week, TargetID: strconv.FormatInt(target.ID, 10), RequestKey: strings.Repeat("c", 32)}
	if _, err := s.SwapFavorite(ctx, permission.Actor.UserID, meal.ID, swap, expired); !errors.Is(err, ErrPersonalPermission) {
		t.Fatal("favorite swap bypassed Premium", err)
	}
	if _, err := s.SwapPersonalRecipe(ctx, permission.Actor, nil, first.ID, first.VersionID, swap, now); !errors.Is(err, ErrPersonalPermission) {
		t.Fatal("direct swap bypassed Premium", err)
	}
	if _, err := s.ReadPersonalRecipe(ctx, permission.Actor, first.ID, first.VersionID, now); err != nil {
		t.Fatal("expiry blocked owned reads", err)
	}
	if _, err := s.RemovePersonalQueuedRecipe(ctx, permission.Actor, blocked.Entries[0].ID, strings.Repeat("d", 32), now); err != nil {
		t.Fatal("Free user could not remove blocked entry", err)
	}
	if _, err := s.QueueFavorite(ctx, permission.Actor.UserID, meal.ID, "2026-11-16", permission); err != nil {
		t.Fatal("reactivation did not allow reviewed queue", err)
	}
	if err := s.DeletePersonalRecipe(ctx, permission.Actor, first.ID, first.VersionID, strings.Repeat("e", 32), now); err != nil {
		t.Fatal(err)
	}
	favorites, err := s.Favorites(ctx, permission.Actor.UserID, 50, 0)
	if err != nil || len(favorites.Recipes) != 0 {
		t.Fatal("deleted source retained linked favorite", err)
	}
	if _, err := s.QueueFavorite(ctx, permission.Actor.UserID, meal.ID, "2026-11-16", permission); err == nil {
		t.Fatal("deleted source requeued via retained meal")
	}
	retained, err := s.GetRecipe(ctx, permission.Actor.UserID, meal.ID)
	if err != nil || retained.Title != first.Content.Title {
		t.Fatal("library deletion rewrote planned meal", err)
	}
}

func TestPersonalQueueKeysCannotBeReusedForRecipeMutations(t *testing.T) {
	s, _, permission, first, now := personalPlanningFixture(t)
	ctx := t.Context()
	key := strings.Repeat("b", 32)
	if _, err := s.QueuePersonalRecipe(ctx, permission.Actor, permission.Entitlements, first.ID, first.VersionID, "2026-11-09", key, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavePersonalRecipe(ctx, permission.Actor, permission.Entitlements, PersonalSave{RequestKey: key, Content: personalDocument("Different action")}, now); !errors.Is(err, ErrPersonalKeyConflict) {
		t.Fatal("assignment key reused for text save", err)
	}
	if err := s.DeletePersonalRecipe(ctx, permission.Actor, first.ID, first.VersionID, key, now); !errors.Is(err, ErrPersonalKeyConflict) {
		t.Fatal("assignment key reused for recipe deletion", err)
	}
}

func TestPersonalQueueConcurrencyRestartRemovalAndTransactionFailure(t *testing.T) {
	s, _, permission, first, now := personalPlanningFixture(t)
	ctx := t.Context()
	week := "2026-11-09"
	key := strings.Repeat("b", 32)
	if err := s.DB.Exec("CREATE TRIGGER fail_personal_queue_receipt BEFORE INSERT ON personal_queue_receipts BEGIN SELECT RAISE(ABORT,'injected queue failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueuePersonalRecipe(ctx, permission.Actor, permission.Entitlements, first.ID, first.VersionID, week, key, now); err == nil {
		t.Fatal("queue receipt fault accepted")
	}
	var count int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipe_queue").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial queue survived", err)
	}
	if err := s.DB.Exec("DROP TRIGGER fail_personal_queue_receipt").Error; err != nil {
		t.Fatal(err)
	}
	other, err := Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	var group sync.WaitGroup
	outcomes := make(chan domain.FavoriteWeek, 8)
	for i := 0; i < 8; i++ {
		group.Go(func() {
			db := s
			if i%2 == 1 {
				db = other
			}
			out, err := db.QueuePersonalRecipe(ctx, permission.Actor, permission.Entitlements, first.ID, first.VersionID, week, key, now)
			if err != nil {
				t.Error(err)
				return
			}
			outcomes <- out
		})
	}
	group.Wait()
	close(outcomes)
	entry := ""
	for out := range outcomes {
		if len(out.Entries) != 1 {
			t.Fatal("duplicate/missing queue")
		}
		if entry == "" {
			entry = out.Entries[0].ID
		} else if entry != out.Entries[0].ID {
			t.Fatal("concurrent retries changed outcome")
		}
	}
	if entry == "" {
		t.Fatal("no queue outcome")
	}
	if _, err := other.RemovePersonalQueuedRecipe(ctx, permission.Actor, entry, strings.Repeat("c", 32), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueuePersonalRecipe(ctx, permission.Actor, permission.Entitlements, first.ID, first.VersionID, week, key, now); !errors.Is(err, ErrPersonalDeleted) {
		t.Fatal("old queue retry recreated a removed entry", err)
	}
	for _, actor := range []PersonalActor{{UserID: 2, SessionHash: "personal-session-2"}, {UserID: 1, SessionHash: "revoked"}} {
		if _, err := s.QueuePersonalRecipe(ctx, actor, permission.Entitlements, first.ID, first.VersionID, week, fmt.Sprintf("%032x", 900+actor.UserID), now); err == nil {
			t.Fatal("foreign/revoked queue allowed")
		}
	}
}

func TestPersonalSwapsKeepFavoriteVersionsAndRollBackAsOneMutation(t *testing.T) {
	s, _, permission, first, now := personalPlanningFixture(t)
	ctx := t.Context()
	week := "2026-11-09"
	var source Meal
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		r, err := personalRecipeSnapshot(tx, permission.Actor, first)
		if err != nil {
			return err
		}
		source, err = copyRecipe(tx, permission.Actor.UserID, nil, week, r)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFavorite(ctx, permission.Actor.UserID, source.ID, true, permission); err != nil {
		t.Fatal(err)
	}
	second, err := s.SavePersonalRecipe(ctx, permission.Actor, permission.Entitlements, PersonalSave{ID: first.ID, ExpectedVersion: first.VersionID, RequestKey: strings.Repeat("b", 32), Content: personalDocument("New library revision")}, now)
	if err != nil {
		t.Fatal(err)
	}
	favorites, err := s.Favorites(ctx, permission.Actor.UserID, 50, 0)
	if err != nil || len(favorites.Recipes) != 1 || favorites.Recipes[0].Title != first.Content.Title || favorites.Recipes[0].Source.VersionID != first.VersionID {
		t.Fatal("edit rewrote favorite version", err)
	}
	target := shoppingMeal(t, s, permission.Actor.UserID, "2026-11-10", "Replace deliberately", "1")
	in := domain.FavoriteSwap{Week: week, TargetID: strconv.FormatInt(target.ID, 10), RequestKey: strings.Repeat("c", 32)}
	if err := s.DB.Exec("CREATE TRIGGER fail_personal_swap BEFORE INSERT ON favorite_swaps BEGIN SELECT RAISE(ABORT,'injected swap failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.SwapFavorite(ctx, permission.Actor.UserID, source.ID, in, permission); err == nil {
		t.Fatal("swap receipt fault accepted")
	}
	if old, err := s.GetRecipe(ctx, permission.Actor.UserID, target.ID); err != nil || old.Title != "Replace deliberately" {
		t.Fatal("swap failure removed old meal", err)
	}
	if err := s.DB.Exec("DROP TRIGGER fail_personal_swap").Error; err != nil {
		t.Fatal(err)
	}
	oldVersion, err := s.SwapFavorite(ctx, permission.Actor.UserID, source.ID, in, permission)
	if err != nil || oldVersion.Source == nil || oldVersion.Source.VersionID != first.VersionID || oldVersion.Title != first.Content.Title {
		t.Fatal("favorite swap silently used latest version", err)
	}
	direct := domain.FavoriteSwap{Week: week, TargetID: oldVersion.ID, RequestKey: strings.Repeat("d", 32)}
	current, err := s.SwapPersonalRecipe(ctx, permission.Actor, permission.Entitlements, second.ID, second.VersionID, direct, now)
	if err != nil || current.Source == nil || current.Source.VersionID != second.VersionID || current.Title != second.Content.Title {
		t.Fatal("direct swap did not use reviewed current version", err)
	}
	if err := s.DeletePersonalRecipe(ctx, permission.Actor, second.ID, second.VersionID, strings.Repeat("e", 32), now); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		replay, err := s.SwapPersonalRecipe(ctx, permission.Actor, nil, second.ID, second.VersionID, direct, now)
		if err != nil || replay.ID != current.ID {
			t.Fatal("committed retry changed an existing assignment", err)
		}
	}
	var count int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM meals WHERE creator_id=?", permission.Actor.UserID).Scan(&count); err != nil || count != 2 {
		t.Fatal("swap replay recreated meals", err)
	}
	retained, err := s.GetRecipe(ctx, permission.Actor.UserID, source.ID)
	if err != nil || retained.Title != first.Content.Title || retained.Source == nil || retained.Source.PhotoID == nil {
		t.Fatal("source meal history changed", err)
	}
}

func TestPersonalAndGeneratedQueuesShareSevenSlotsWithoutExtraCommercialGates(t *testing.T) {
	s, _, permission, first, now := personalPlanningFixture(t)
	ctx := t.Context()
	week := "2026-11-09"
	if _, err := s.QueuePersonalRecipe(ctx, permission.Actor, permission.Entitlements, first.ID, first.VersionID, week, strings.Repeat("b", 32), now); err != nil {
		t.Fatal(err)
	}
	free := permission
	free.Entitlements = nil
	for i := 0; i < 7; i++ {
		meal := shoppingMeal(t, s, permission.Actor.UserID, "2026-11-02", fmt.Sprintf("Generated favorite %d", i), "1")
		if _, err := s.SetFavorite(ctx, permission.Actor.UserID, meal.ID, true); err != nil {
			t.Fatal(err)
		}
		queued, err := s.QueueFavorite(ctx, permission.Actor.UserID, meal.ID, week, free)
		if i < 6 {
			if err != nil || len(queued.Recipes) != i+2 {
				t.Fatal("generated favorite gained a Premium gate", err)
			}
		} else if !errors.Is(err, ErrFavoriteConflict) {
			t.Fatal("mixed queue exceeded seven slots", err)
		}
	}
	second, err := s.SavePersonalRecipe(ctx, permission.Actor, permission.Entitlements, PersonalSave{RequestKey: strings.Repeat("c", 32), Content: personalDocument("Another private dish")}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueuePersonalRecipe(ctx, permission.Actor, permission.Entitlements, second.ID, second.VersionID, week, strings.Repeat("d", 32), now); !errors.Is(err, ErrFavoriteConflict) {
		t.Fatal("personal queue ignored occupied generated slots", err)
	}
}

func TestPersonalPlanRechecksEntitlementAfterCapture(t *testing.T) {
	s, _, permission, recipe, now := personalPlanningFixture(t)
	ctx := t.Context()
	if err := s.DB.Exec("UPDATE users SET email='plan-entitlement@example.test', email_key='plan-entitlement@example.test' WHERE user_id=?", permission.Actor.UserID).Error; err != nil {
		t.Fatal(err)
	}
	reader, revoke := testEntitlementSwitch(t, s, permission.Actor, entitlementIdentity(t, s, permission.Actor.UserID), fixedEntitlementClock(now))
	permission.Entitlements = reader
	week := "2026-11-09"
	queued, err := s.QueuePersonalRecipe(ctx, permission.Actor, permission.Entitlements, recipe.ID, recipe.VersionID, week, strings.Repeat("b", 32), now)
	if err != nil {
		t.Fatal(err)
	}
	input := domain.MenuInput{Dates: []string{week}, Adults: 2, MaxMinutes: 30, Budget: "low", Region: "BE", Creativity: 3, FavoriteRevisions: map[string]string{week: queued.Revision}}
	var captured []domain.PlannedFavorite
	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		captured, err = CaptureFavorites(tx, permission.Actor.UserID, input, func([]string) {}, permission)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(domain.GenerationRequest{MenuInput: input, Favorites: captured})
	op := Generation{UserID: permission.Actor.UserID, Key: "captured-before-revocation", RequestHash: "hash", RequestJSON: string(request), Status: "running", Stage: "saving"}
	if err := s.DB.Create(&op).Error; err != nil {
		t.Fatal(err)
	}
	revoke()
	if _, err := s.SaveMenu(ctx, permission.Actor.UserID, op.ID, input, nil, reader); !errors.Is(err, ErrPersonalPermission) {
		t.Fatal("plan used Premium captured at admission", err)
	}
	var menus, consumed int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM menus WHERE user_id=?", permission.Actor.UserID).Scan(&menus); err != nil || menus != 0 {
		t.Fatal("denied plan wrote a menu", menus, err)
	}
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipe_queue WHERE consumed_operation IS NOT NULL").Scan(&consumed); err != nil || consumed != 0 {
		t.Fatal("denied plan consumed queued personal recipe", consumed, err)
	}
}
