package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMealsAreChronologicalAcrossHistoryPagesAndSavedMenus(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "order.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	user := User{FirstName: "Order", LastName: "Chef", Email: "order@example.test", Username: "order", Password: "hash"}
	if err = s.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	menu := MenuRecord{UserID: user.ID}
	if err = s.DB.Create(&menu).Error; err != nil {
		t.Fatal(err)
	}
	// Insert dates out of order, including a tie at the pagination boundary.
	for _, date := range []string{"2026-10-30", "2026-10-28", "2026-10-28", "2026-10-29"} {
		meal := Meal{CreatorID: user.ID, MenuID: &menu.ID, CreationDate: date, RecipeTitle: date, ImageStatus: "unavailable"}
		if err = s.DB.Create(&meal).Error; err != nil {
			t.Fatal(err)
		}
	}
	var ids []string
	for offset := 0; ; {
		page, e := s.History(ctx, user.ID, "2026-10-26", "2026-11-02", 1, offset)
		if e != nil {
			t.Fatal(e)
		}
		for _, recipe := range page.Recipes {
			ids = append(ids, recipe.ID)
		}
		if page.NextOffset == nil {
			break
		}
		offset = *page.NextOffset
	}
	want := []string{"2", "3", "4", "1"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("history order = %v; want %v", ids, want)
	}
	saved, err := s.GetMenu(ctx, user.ID, menu.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids = nil
	for _, recipe := range saved.Recipes {
		ids = append(ids, recipe.ID)
	}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("saved menu order = %v; want %v", ids, want)
	}
}
