package menus

import (
	"context"
	"strconv"
	"testing"

	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
)

func TestExistingWeekRequiresConsentBeforeProviderAndFailureKeepsMenu(t *testing.T) {
	ctx := context.Background()
	db, user := workerStore(t)
	provider := &counted{}
	service, err := New(db, provider, providers.Fixture{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	first, err := service.Start(ctx, user, "first-week-menu", request("Belgium"))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(first.ID, 10, 64)
	if done := waitOperation(t, service, user, id); done.Status != "succeeded" {
		t.Fatal(done)
	}
	before, err := db.History(ctx, user, "2026-12-28", "2027-01-04", 25, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(ctx, user, "without-consent", request("Belgium")); err == nil {
		t.Fatal("existing week accepted without confirmation")
	}
	if provider.calls.Load() != 1 {
		t.Fatal("missing consent invoked the provider")
	}
	failed := request("__quota")
	failed.ReplaceExisting = true
	op, err := service.Start(ctx, user, "failed-week-replacement", failed)
	if err != nil {
		t.Fatal(err)
	}
	id, _ = strconv.ParseInt(op.ID, 10, 64)
	if done := waitOperation(t, service, user, id); done.Status != "failed" {
		t.Fatal(done)
	}
	after, err := db.History(ctx, user, "2026-12-28", "2027-01-04", 25, 0)
	if err != nil || len(after.Recipes) != 1 || after.Recipes[0].ID != before.Recipes[0].ID {
		t.Fatal("failed replacement changed the original menu", err)
	}
	replacement := request("Belgium")
	replacement.ReplaceExisting = true
	op, err = service.Start(ctx, user, "confirmed-week-replacement", replacement)
	if err != nil {
		t.Fatal(err)
	}
	id, _ = strconv.ParseInt(op.ID, 10, 64)
	if done := waitOperation(t, service, user, id); done.Status != "succeeded" {
		t.Fatal(done)
	}
	after, err = db.History(ctx, user, "2026-12-28", "2027-01-04", 25, 0)
	if err != nil || len(after.Recipes) != 1 || after.Recipes[0].ID == before.Recipes[0].ID {
		t.Fatal("confirmed replacement did not replace the old menu", err)
	}
	replayed, err := service.Start(ctx, user, "confirmed-week-replacement", replacement)
	if err != nil || replayed.ID != op.ID || provider.calls.Load() != 3 {
		t.Fatal("idempotent replacement called provider again", err)
	}
}

func TestMenuAppearingDuringGenerationIsNotOverwrittenWithoutConsent(t *testing.T) {
	ctx := context.Background()
	db, user := workerStore(t)
	service, err := New(db, &counted{}, providers.Fixture{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	op, err := service.Start(ctx, user, "stale-save-request", request("__slow"))
	if err != nil {
		t.Fatal(err)
	}
	meal := store.Meal{CreatorID: user, CreationDate: "2026-12-31", RecipeTitle: "Keep this meal", ImageStatus: "unavailable"}
	if err = db.DB.Create(&meal).Error; err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(op.ID, 10, 64)
	done := waitOperation(t, service, user, id)
	if done.Status != "failed" || done.ErrorCode == nil || *done.ErrorCode != "menu_exists" {
		t.Fatalf("stale save was not refused: %+v", done)
	}
	page, err := db.History(ctx, user, "2026-12-28", "2027-01-04", 25, 0)
	if err != nil || len(page.Recipes) != 1 || page.Recipes[0].Title != "Keep this meal" {
		t.Fatal("stale save changed current meals", err)
	}
}
