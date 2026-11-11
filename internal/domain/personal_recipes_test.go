package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func personalContent() PersonalRecipeContent {
	quantity, unit := Decimal("0.2500"), " kg "
	return PersonalRecipeContent{
		SchemaVersion: 1, Title: "  Family dinner  ", Description: "A personal recipe.",
		Servings: 4, PrepMinutes: 10, CookMinutes: 20, TotalMinutes: 40,
		Ingredients:  []IngredientContent{{Name: "  Chickpeas  ", Quantity: &quantity, Unit: &unit}},
		Instructions: []string{"  Heat the pan.\r\nCook gently.  "},
	}
}

func TestPersonalRecipeNormalizationPreservesExactContent(t *testing.T) {
	in := personalContent()
	before, _ := json.Marshal(in)
	out, err := in.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if out.Title != "Family dinner" || out.Ingredients[0].Name != "Chickpeas" || *out.Ingredients[0].Quantity != "0.25" || *out.Ingredients[0].Unit != "kg" || out.Instructions[0] != "Heat the pan.\nCook gently." || out.TotalMinutes != 40 {
		t.Fatal("presentation normalization changed recipe meaning")
	}
	if out.Tips == nil || out.Allergens == nil || out.Ingredients == nil || out.Instructions == nil {
		t.Fatal("canonical arrays must be present")
	}
	after, _ := json.Marshal(in)
	if string(before) != string(after) {
		t.Fatal("normalization mutated editor input")
	}
	large := Decimal("123456789012345678901234567890.0000000000000000000000010")
	in.Ingredients[0].Quantity = &large
	out, err = in.Normalize()
	if err != nil || *out.Ingredients[0].Quantity != "123456789012345678901234567890.000000000000000000000001" {
		t.Fatal("quantity lost decimal precision", err)
	}
	in.Title = "<script>personal text</script>"
	out, err = in.Normalize()
	if err != nil || out.Title != in.Title {
		t.Fatal("personal text must remain text, not be rewritten", err)
	}
}

func TestPersonalRecipeValidationHasFieldErrorsAndLimits(t *testing.T) {
	tests := []struct {
		name, field string
		change      func(*PersonalRecipeContent)
	}{
		{"schema", "schemaVersion", func(v *PersonalRecipeContent) { v.SchemaVersion = 2 }},
		{"empty title", "title", func(v *PersonalRecipeContent) { v.Title = " \n " }},
		{"title bound", "title", func(v *PersonalRecipeContent) { v.Title = strings.Repeat("é", 201) }},
		{"invalid Unicode", "title", func(v *PersonalRecipeContent) { v.Title = string([]byte{0xff}) }},
		{"control", "description", func(v *PersonalRecipeContent) { v.Description = "private\x00value" }},
		{"single line", "title", func(v *PersonalRecipeContent) { v.Title = "one\ntwo" }},
		{"servings", "servings", func(v *PersonalRecipeContent) { v.Servings = 0 }},
		{"servings max", "servings", func(v *PersonalRecipeContent) { v.Servings = 41 }},
		{"negative time", "prepMinutes", func(v *PersonalRecipeContent) { v.PrepMinutes = -1 }},
		{"time bound", "cookMinutes", func(v *PersonalRecipeContent) { v.CookMinutes = 1441 }},
		{"total before sum", "totalMinutes", func(v *PersonalRecipeContent) { v.TotalMinutes = 29 }},
		{"total bound", "totalMinutes", func(v *PersonalRecipeContent) { v.TotalMinutes = 2881 }},
		{"no ingredients", "ingredients", func(v *PersonalRecipeContent) { v.Ingredients = nil }},
		{"ingredient count", "ingredients", func(v *PersonalRecipeContent) { v.Ingredients = make([]IngredientContent, 101) }},
		{"ingredient name", "ingredients.0.name", func(v *PersonalRecipeContent) { v.Ingredients[0].Name = "" }},
		{"negative quantity", "ingredients.0.quantity", func(v *PersonalRecipeContent) { q := Decimal("-1"); v.Ingredients[0].Quantity = &q }},
		{"quantity control", "ingredients.0.quantity", func(v *PersonalRecipeContent) { q := Decimal("1\n"); v.Ingredients[0].Quantity = &q }},
		{"exponent quantity", "ingredients.0.quantity", func(v *PersonalRecipeContent) { q := Decimal("1e3"); v.Ingredients[0].Quantity = &q }},
		{"fraction quantity", "ingredients.0.quantity", func(v *PersonalRecipeContent) { q := Decimal("1/3"); v.Ingredients[0].Quantity = &q }},
		{"quantity bound", "ingredients.0.quantity", func(v *PersonalRecipeContent) { q := Decimal(strings.Repeat("1", 101)); v.Ingredients[0].Quantity = &q }},
		{"unit bound", "ingredients.0.unit", func(v *PersonalRecipeContent) { u := strings.Repeat("x", 81); v.Ingredients[0].Unit = &u }},
		{"no instructions", "instructions", func(v *PersonalRecipeContent) { v.Instructions = nil }},
		{"instruction bound", "instructions.0", func(v *PersonalRecipeContent) { v.Instructions[0] = strings.Repeat("x", 4001) }},
		{"empty instruction", "instructions.0", func(v *PersonalRecipeContent) { v.Instructions[0] = " " }},
		{"tip bound", "tips.0", func(v *PersonalRecipeContent) { v.Tips = []string{strings.Repeat("x", 1001)} }},
		{"allergen count", "allergens", func(v *PersonalRecipeContent) { v.Allergens = make([]string, 31) }},
		{"payload", "recipe", func(v *PersonalRecipeContent) {
			v.Instructions = make([]string, 100)
			for i := range v.Instructions {
				v.Instructions[i] = strings.Repeat("x", 3000)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := personalContent()
			tc.change(&v)
			_, err := v.Normalize()
			var fields *PersonalRecipeValidationError
			if !errors.As(err, &fields) || fields.Fields[tc.field] == "" {
				t.Fatalf("expected field %s, got %v", tc.field, err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("validation diagnostic contains recipe content")
			}
		})
	}
	v := personalContent()
	v.Title = strings.Repeat("é", 200)
	v.Description, v.Equipment = "", ""
	v.Ingredients[0].Quantity, v.Ingredients[0].Unit = nil, nil
	if _, err := v.Normalize(); err != nil {
		t.Fatal("valid Unicode and optional fields rejected", err)
	}
}

func TestPersonalCopyKeepsSourceValuesAndRequiresUnambiguousServings(t *testing.T) {
	description, equipment, note, quantity, unit := "A source description.", "Pan", "User declaration.", "0.000001", "g"
	prep, cook, total := 5, 10, 25
	source := Recipe{Title: "Source", Note: &description, Equipment: &equipment,
		Yield: []string{"4 servings"}, Instructions: []string{"Cook."}, Tips: []string{"Rest."},
		Allergens: []string{"Milk"}, AllergenNote: &note, PrepMinutes: &prep, CookMinutes: &cook, TotalMinutes: &total,
		Ingredients: []Ingredient{{Name: "Butter", Quantity: &quantity, Unit: &unit}}}
	before, _ := json.Marshal(source)
	draft := PersonalRecipeDraft(source)
	if draft.NeedsServings || draft.Content.Servings != 4 || draft.Content.TotalMinutes != 25 || !reflect.DeepEqual(draft.SourceYield, source.Yield) {
		t.Fatal("copy changed source yield or waiting time")
	}
	if _, err := draft.Content.Normalize(); err != nil {
		t.Fatal(err)
	}
	draft.Content.Instructions[0] = "Edited."
	*draft.Content.Ingredients[0].Quantity = "9"
	draft.SourceYield[0] = "Edited yield"
	after, _ := json.Marshal(source)
	if string(before) != string(after) {
		t.Fatal("editing draft mutated source")
	}
	source.Yield = []string{"2 adults", "2 children"}
	draft = PersonalRecipeDraft(source)
	if !draft.NeedsServings || draft.Content.Servings != 0 || !reflect.DeepEqual(draft.SourceYield, source.Yield) {
		t.Fatal("ambiguous source yield silently inferred")
	}
	if _, err := draft.Content.Normalize(); err == nil {
		t.Fatal("unconfirmed servings accepted")
	}
}

func TestPersonalContentDoesNotRelaxGeneratedRecipeValidation(t *testing.T) {
	v := personalContent()
	v.Ingredients[0].Name, v.Allergens = "Butter", []string{"Milk"}
	normalized, err := v.Normalize()
	if err != nil {
		t.Fatal("explicit personal choices must not be rewritten", err)
	}
	content := normalized.RecipeContent()
	if content.TotalMinutes != 40 || content.AllergenNote == "" || content.Yield[0] != "4 servings" {
		t.Fatal("snapshot conversion lost required values")
	}
	preferences := EmptyPreferences()
	preferences.Allergens = []string{"Milk"}
	input := MenuInput{MaxMinutes: 60}
	if content.Validate(input, preferences) == nil {
		t.Fatal("generated-recipe exclusions were relaxed")
	}
}
