package domain

import "testing"

func validRecipe(ingredient string) RecipeContent {
	q := Decimal("1")
	return RecipeContent{Title: "Dinner", Note: "A meal", Yield: []string{"2 servings"}, Equipment: "Pan", Instructions: []string{"Cook the vegetables."}, Tips: []string{}, Allergens: []string{}, AllergenNote: "Check ingredient labels.", PrepMinutes: 5, CookMinutes: 10, TotalMinutes: 15, Ingredients: []IngredientContent{{Name: ingredient, Quantity: &q}}}
}
func TestKnownConflictsAndFalsePositiveBoundaries(t *testing.T) {
	input := MenuInput{Dates: []string{"2026-10-27"}, Adults: 2, MaxMinutes: 30, Budget: "low", Region: "Belgium"}
	for _, pair := range [][2]string{{"Milk", "Cream"}, {"Peanuts", "Peanut oil"}, {"Soybeans", "Tofu"}, {"Sesame seeds", "Tahini"}} {
		t.Run(pair[0], func(t *testing.T) {
			p := EmptyPreferences()
			p.Allergens = []string{pair[0]}
			if validRecipe(pair[1]).Validate(input, p) == nil {
				t.Fatal("known allergen accepted")
			}
		})
	}
	p := EmptyPreferences()
	p.Allergens = []string{"Milk"}
	if err := validRecipe("Butternut squash").Validate(input, p); err != nil {
		t.Fatalf("unrelated word rejected: %v", err)
	}
	p = EmptyPreferences()
	p.Diet = []string{"Vegetarian"}
	if validRecipe("Beef mince").Validate(input, p) == nil {
		t.Fatal("a false classification flag bypassed a known dietary conflict")
	}
	p = EmptyPreferences()
	p.Diet = []string{"Gluten-Free"}
	if err := validRecipe("Gluten-free pasta").Validate(input, p); err != nil {
		t.Fatalf("explicit alternative rejected: %v", err)
	}
	r := validRecipe("Gluten-free pasta")
	r.Allergens = []string{"Cereals containing gluten"}
	if r.Validate(input, p) == nil {
		t.Fatal("declared allergen bypassed validation")
	}
}
