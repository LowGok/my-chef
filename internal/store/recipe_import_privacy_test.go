package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LowGok/my-chef/internal/domain"
)

func TestRecipeImportShoppingPrivacyComesFromSavedSnapshots(t *testing.T) {
	s, actor, _, premium, now := personalFixture(t)
	vault := importVault(t)
	_, input := readyImportSave(t, s, actor, premium, vault, strings.Repeat("a", 32), now)
	saved, err := s.SavePersonalRecipe(t.Context(), actor, premium, input, now)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := s.QueuePersonalRecipe(t.Context(), actor, premium, saved.ID, saved.VersionID, "2026-11-09", strings.Repeat("c", 32), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := copyRecipe(s.DB.WithContext(t.Context()), actor.UserID, nil, "2026-11-09", queued.Recipes[0]); err != nil {
		t.Fatal(err)
	}
	list, err := s.Shopping(t.Context(), actor.UserID, "2026-11-09")
	if err != nil || len(list.Meals) != 1 {
		t.Fatal("planned imported meal unavailable", err)
	}
	private := func(value domain.ShoppingList) bool {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			PrivateImports bool `json:"privateImports"`
		}
		if json.Unmarshal(raw, &decoded) != nil {
			t.Fatal("invalid shopping response")
		}
		return decoded.PrivateImports
	}
	if !private(list) {
		t.Fatal("imported snapshot was offered for durable browser caching")
	}
	list.Meals[0].Selected = false
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	var forged map[string]any
	if json.Unmarshal(raw, &forged) != nil {
		t.Fatal("invalid test packet")
	}
	forged["privateImports"] = false
	raw, err = json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &list) != nil {
		t.Fatal("invalid test packet")
	}
	result, err := s.SaveShopping(t.Context(), actor.UserID, "2026-11-09", list)
	if err != nil || !private(result) {
		t.Fatal("deselection or client flags removed imported-title privacy", err)
	}
}
