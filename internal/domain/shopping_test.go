package domain

import (
	"encoding/json"
	"os"
	"testing"
)

func str(v string) *string { return &v }

func TestShoppingExactTotalsAndContributions(t *testing.T) {
	data, err := os.ReadFile("testdata/shopping.json")
	if err != nil {
		t.Fatal(err)
	}
	var recipes []Recipe
	if err = json.Unmarshal(data, &recipes); err != nil {
		t.Fatal(err)
	}
	items := ShoppingIngredients(recipes)
	if len(items) != 25 {
		t.Fatalf("want 25 identities, got %d", len(items))
	}
	expected := map[string]string{"carrot": "450", "garlic": "30", "olive oil": "75"}
	for _, item := range items {
		if q, ok := expected[item.Name]; ok {
			if item.Quantity == nil || *item.Quantity != q {
				t.Fatalf("%s: %#v", item.Name, item.Quantity)
			}
			if len(item.Contributions) < 3 {
				t.Fatalf("missing recipe trace: %+v", item)
			}
			delete(expected, item.Name)
		}
	}
	if len(expected) != 0 {
		t.Fatal(expected)
	}
}

func TestShoppingNormalizationAndAmbiguity(t *testing.T) {
	recipes := []Recipe{
		{ID: "1", Title: "Soup", Ingredients: []Ingredient{
			{ID: "1", Name: " Carrot ", Quantity: str("0.1"), Unit: str("kg")},
			{ID: "2", Name: "olive oil", Quantity: str("0.1"), Unit: str("l")},
			{ID: "3", Name: "salt", Quantity: nil, Unit: str("pinch")},
			{ID: "4", Name: "beans, drained", Quantity: str("100"), Unit: str("g")},
			{ID: "5", Name: "carrot", Quantity: str("2"), Unit: str("pieces")},
			{ID: "6", Name: "spice", Quantity: str("to taste"), Unit: nil},
			{ID: "7", Name: "onion", Quantity: str("1/2"), Unit: str("piece")},
			{ID: "8", Name: "spice", Quantity: str("2"), Unit: str("scoop")},
		}},
		{ID: "2", Title: "Stew", Ingredients: []Ingredient{
			{ID: "9", Name: "CARROT", Quantity: str("0.25"), Unit: str("grams")},
			{ID: "10", Name: "olive oil", Quantity: str("0.2"), Unit: str("millilitres")},
			{ID: "11", Name: "salt", Quantity: nil, Unit: str("pinch")},
			{ID: "12", Name: "beans", Quantity: str("100"), Unit: str("g")},
			{ID: "13", Name: "onion", Quantity: str("0.25"), Unit: str("piece")},
			{ID: "14", Name: "spice", Quantity: str("3"), Unit: str("scoop")},
		}},
	}
	items := ShoppingIngredients(recipes)
	if len(items) != 11 {
		t.Fatalf("identities merged unsafely: %d %+v", len(items), items)
	}
	totals := map[string]string{"carrot|g": "100.25", "olive oil|ml": "100.2", "onion|piece": "0.75"}
	for _, v := range items {
		if q, ok := totals[v.Name+"|"+v.Unit]; ok {
			if v.Quantity == nil || *v.Quantity != q || len(v.Contributions) != 2 {
				t.Fatalf("bad exact total: %+v", v)
			}
			delete(totals, v.Name+"|"+v.Unit)
		}
	}
	if len(totals) != 0 {
		t.Fatal(totals)
	}
	again := ShoppingIngredients(recipes)
	for i, v := range items {
		if v.ID != again[i].ID {
			t.Fatal("unstable identities")
		}
	}
}

func TestShoppingKeepsUnsupportedAmountSyntaxReadable(t *testing.T) {
	items := ShoppingIngredients([]Recipe{{ID: "legacy", Title: "Legacy", Ingredients: []Ingredient{{Name: "salt", Quantity: str("1e4"), Unit: str("g")}, {Name: "salt", Quantity: str("2"), Unit: str("g")}}}})
	if len(items) != 2 {
		t.Fatal("unsupported amount syntax was combined")
	}
	found := false
	for _, item := range items {
		if item.Quantity != nil && *item.Quantity == "1e4" {
			found = true
		}
	}
	if !found {
		t.Fatal("ambiguous source amount was rewritten")
	}
}
