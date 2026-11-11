package imports

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func structuredRecipe(name string) map[string]any {
	return map[string]any{
		"@context": "https://schema.org", "@type": "Recipe", "name": name,
		"description": "A simple dinner.", "recipeYield": "4 servings",
		"prepTime": "PT10M", "cookTime": "PT20M", "totalTime": "PT35M",
		"recipeIngredient":   []string{"1/2 tsp salt", "2 eggs"},
		"recipeInstructions": []any{map[string]any{"@type": "HowToStep", "text": "Heat the pan."}, map[string]any{"@type": "HowToStep", "text": "Cook gently."}},
		"allergens":          []string{"Egg"}, "image": "https://images.example.com/private-image-canary.jpg",
	}
}

func inspectFixture(t *testing.T, objects ...any) PageInspection {
	t.Helper()
	var page strings.Builder
	page.WriteString("<html><head>")
	for _, object := range objects {
		raw, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		page.WriteString(`<script type="application/ld+json">`)
		page.Write(raw)
		page.WriteString("</script>")
	}
	page.WriteString("</head><body><main><h1>A written recipe</h1><p>Ingredients and method for a family dinner.</p></main></body></html>")
	result, err := InspectRecipePage(RecipePage{URL: "https://recipes.example.com/recipe", HTML: []byte(page.String())}, sourceFixture())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestStructuredRecipeMapsWithoutInventingOrRetainingImages(t *testing.T) {
	result := inspectFixture(t, structuredRecipe("Chickpea omelette"))
	if len(result.Candidates) != 1 || !result.Candidates[0].Complete || result.FallbackText != "" {
		t.Fatal("complete structured recipe did not take the deterministic path")
	}
	draft, err := result.Select("")
	if err != nil {
		t.Fatal(err)
	}
	if draft.Content.Title != "Chickpea omelette" || draft.Content.Servings != 4 || draft.Content.TotalMinutes != 35 || draft.Content.PrepMinutes != 10 || *draft.Content.Ingredients[0].Quantity != "0.5" || *draft.Content.Ingredients[0].Unit != "tsp" || draft.Content.Ingredients[1].Name != "eggs" {
		t.Fatal("structured mapping changed source meaning")
	}
	if draft.Source.URL != "https://recipes.example.com/recipe" {
		t.Fatal("untrusted metadata changed provenance")
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "private-image-canary") {
		t.Fatal("source image survived structured extraction")
	}
	if len(draft.Issues) != 1 || draft.Issues[0].Field != "allergens" {
		t.Fatal("allergen declaration was treated as verified")
	}
}

func TestStructuredRecipeGraphsArraysAndExplicitSelection(t *testing.T) {
	one, two := structuredRecipe("First dish"), structuredRecipe("Second dish")
	two["@type"] = []string{"Thing", "https://schema.org/Recipe"}
	result := inspectFixture(t, map[string]any{"@graph": []any{one, two}})
	if len(result.Candidates) != 2 {
		t.Fatal("graph recipes were lost or merged")
	}
	if _, err := result.Select(""); !errors.Is(err, ErrImportSelectionRequired) {
		t.Fatal("ambiguous recipe silently selected")
	}
	chosen, err := result.Select(result.Candidates[1].ID)
	if err != nil || chosen.Content.Title != "Second dish" {
		t.Fatal("explicit recipe selection failed", err)
	}
	chosen.Content.Instructions[0] = "Changed locally"
	if result.Candidates[1].Draft.Content.Instructions[0] == "Changed locally" {
		t.Fatal("editor mutated retained candidate")
	}
	if result.FallbackText != "" {
		t.Fatal("multiple recipes were combined into fallback input")
	}
	if _, err := result.Select("not-a-candidate"); !errors.Is(err, ErrImportSelectionRequired) {
		t.Fatal("unknown candidate accepted")
	}
	array := inspectFixture(t, []any{one, one}, one)
	if len(array.Candidates) != 1 {
		t.Fatal("identical structured entities were duplicated")
	}
}

func TestStructuredRecipeKeepsAmbiguousYieldAndDurationsForReview(t *testing.T) {
	recipe := structuredRecipe("Review this dish")
	recipe["recipeYield"] = []string{"2 adults", "2 children"}
	recipe["prepTime"], recipe["cookTime"] = "PT45S", "PT0M"
	recipe["recipeIngredient"] = []string{"1/3 cup rice", "2–3 tomatoes"}
	result := inspectFixture(t, recipe)
	if len(result.Candidates) != 1 || result.Candidates[0].Complete {
		t.Fatal("uncertain structured data was declared complete")
	}
	draft := result.Candidates[0].Draft
	if draft.Content.Servings != 0 || draft.Content.PrepMinutes != 0 || draft.Content.CookMinutes != 0 || draft.Content.Ingredients[0].Quantity != nil || draft.Content.Ingredients[1].Quantity != nil {
		t.Fatal("uncertain quantities, yield or seconds were rounded or guessed")
	}
	found := false
	for _, issue := range draft.Issues {
		if issue.Field == "prepMinutes" && issue.SourceText == "PT45S" {
			found = true
		}
	}
	if !found || len(draft.SourceYield) != 2 {
		t.Fatal("ambiguous source values were discarded")
	}
}

func TestStructuredRecipeInstructionSectionsPreserveOrderAsPlainText(t *testing.T) {
	recipe := structuredRecipe("Sectioned dish")
	recipe["recipeInstructions"] = []any{map[string]any{"@type": "HowToSection", "name": "Sauce", "itemListElement": []any{
		map[string]any{"@type": "HowToStep", "text": "<p>Chop &amp; stir.</p><script>hidden-script-canary</script>"},
		map[string]any{"@type": "HowToStep", "text": "Simmer."},
	}}}
	result := inspectFixture(t, recipe)
	steps := result.Candidates[0].Draft.Content.Instructions
	if len(steps) != 2 || !strings.Contains(steps[0], "Sauce") || !strings.Contains(steps[0], "Chop & stir.") || steps[1] != "Simmer." || strings.Contains(strings.Join(steps, " "), "hidden-script-canary") {
		t.Fatal("instruction sections lost order/context or retained executable content")
	}
}

func TestRecipeFallbackContainsOnlyBoundedReadableContent(t *testing.T) {
	html := `<html><head><script>head-script-canary</script><style>style-canary</style></head><body><nav>navigation-canary</nav><main><h1>Chickpeas</h1><p>For four people.</p><p hidden>hidden-canary</p><p style="display:none">css-canary</p><p aria-hidden="true">aria-canary</p><script>body-script-canary</script><p>Cook gently for twenty minutes.</p></main><footer>footer-canary</footer></body></html>`
	result, err := InspectRecipePage(RecipePage{URL: "https://recipes.example.com/recipe", HTML: []byte(html)}, sourceFixture())
	if err != nil || len(result.Candidates) != 0 || !strings.Contains(result.FallbackText, "Chickpeas") || !strings.Contains(result.FallbackText, "Cook gently") {
		t.Fatal("readable fallback missing", err)
	}
	for _, private := range []string{"script-canary", "style-canary", "navigation-canary", "hidden-canary", "css-canary", "aria-canary", "footer-canary", "<p>"} {
		if strings.Contains(result.FallbackText, private) {
			t.Fatal("hidden/markup/non-recipe content reached fallback")
		}
	}
	if len(result.FallbackText) > maxRecipeFallbackBytes {
		t.Fatal("fallback text exceeded its bound")
	}
}

func TestStructuredRecipeRejectsDuplicateKeysAndPathologicalDocuments(t *testing.T) {
	for _, raw := range []string{
		`{"@type":"Recipe","name":"one","name":"two"}`,
		strings.Repeat("[", 40) + `{}` + strings.Repeat("]", 40),
		`{"@type":"Recipe","recipeIngredient":[` + strings.Repeat(`"ingredient",`, 200) + `"last"]}`,
	} {
		body := `<html><script type="application/ld+json">` + raw + `</script><main><p>Read this written recipe manually.</p></main></html>`
		result, err := InspectRecipePage(RecipePage{URL: "https://recipes.example.com/recipe", HTML: []byte(body)}, sourceFixture())
		if err != nil || len(result.Candidates) != 0 || !result.StructuredInvalid {
			t.Fatal("ambiguous or pathological JSON-LD accepted")
		}
	}
	if _, err := InspectRecipePage(RecipePage{URL: "https://recipes.example.com/recipe", HTML: []byte(strings.Repeat("x", maxRecipeHTMLBytes+1))}, sourceFixture()); !errors.Is(err, ErrImportInput) {
		t.Fatal("oversized HTML accepted")
	}
}

func TestStructuredIngredientParsingKeepsExactFractions(t *testing.T) {
	for _, tc := range []struct{ line, quantity, name string }{
		{"½ tsp salt", "0.5", "salt"}, {"1½ cups oats", "1.5", "oats"}, {"1 1/4 kg potatoes", "1.25", "potatoes"},
		{"0.000000000000000001 g spice", "0.000000000000000001", "spice"}, {"2 eggs", "2", "eggs"},
	} {
		item := ingredientFromText(tc.line)
		if item.Quantity == nil || string(*item.Quantity) != tc.quantity || item.Name != tc.name || item.SourceText != tc.line {
			t.Fatal("ingredient precision or source text changed")
		}
	}
}

func TestStructuredRecipePreservesEquipmentAndPlainStepBoundaries(t *testing.T) {
	recipe := structuredRecipe("Readable recipe")
	recipe["tool"] = []any{map[string]any{"@type": "HowToTool", "name": "Pan"}, "Whisk"}
	recipe["recipeInstructions"] = "Mix gently.\nCook for twenty minutes."
	recipe["recipeYield"] = json.Number("4.0")
	result := inspectFixture(t, recipe)
	if len(result.Candidates) != 1 {
		t.Fatal("valid structured recipe was discarded")
	}
	content := result.Candidates[0].Draft.Content
	if content.Equipment != "Pan, Whisk" || len(content.Instructions) != 2 || content.Servings != 4 {
		t.Fatal("structured equipment, explicit line breaks or exact integer yield were lost")
	}
}

func TestRecipeFallbackHonorsFirstHiddenAttributeAndSingleEntityDecode(t *testing.T) {
	body := `<main><h1>Recipe</h1><p style="display:none" style="display:block">duplicate-attribute-canary</p><p aria-hidden="true" aria-hidden="false">duplicate-aria-canary</p><p>&amp;lt; remains text.</p></main>`
	result, err := InspectRecipePage(RecipePage{URL: "https://recipes.example.com/recipe", HTML: []byte(body)}, sourceFixture())
	if err != nil || strings.Contains(result.FallbackText, "duplicate-") || !strings.Contains(result.FallbackText, "&lt; remains text.") {
		t.Fatal("hidden attributes or entity decoding changed readable content")
	}
}

func TestStructuredRecipeDoesNotTreatContextDefinitionsAsRecipes(t *testing.T) {
	result := inspectFixture(t, map[string]any{"@type": "Thing", "@context": map[string]any{"definition": structuredRecipe("Not a page recipe")}})
	if len(result.Candidates) != 0 {
		t.Fatal("context definitions were treated as page recipe entities")
	}
}
