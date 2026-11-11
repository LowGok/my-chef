package imports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
)

// FixtureExtractionPolicy is used only by explicitly expiring private tests.
// Its key/project are synthetic and never authorize a real provider request.
func FixtureExtractionPolicy(until time.Time) (ExtractionBinding, ExtractionPolicy) {
	binding := ExtractionBinding{ProjectID: "synthetic-free-project", Model: extractionModel, APIKey: "synthetic-key-never-sent"}
	sum := sha256.Sum256([]byte(binding.APIKey))
	policy := ExtractionPolicy{Enabled: true, Evidence: ExtractionEligibility{ProjectID: binding.ProjectID, Model: binding.Model, KeySHA256: hex.EncodeToString(sum[:]), CheckedAt: time.Now().UTC(), ExpiresAt: until.UTC(), FreeTier: true, BillingUnlinked: true, ClientUseEligible: true, ImageTextSchemaSupported: true, FreeCallsPerMinute: 10, FreeCallsPerDay: 200, FreeTokensPerMinute: 100000}, Limits: ExtractionLimits{MaxImages: 3, MaxImageBytes: 2 << 20, MaxTotalImageBytes: 6 << 20, MaxImagePixels: 2048 * 2048, MaxTextBytes: 20000, MaxInputTokens: 32768, MaxOutputTokens: 8192, MaxThinkingTokens: 0, AccountCallsPerDay: 20, SourceCallsPerDay: 40, FleetCallsPerMinute: 2, FleetCallsPerDay: 100, TokensPerMinute: 100000, MaxConcurrent: 2}}
	return binding, policy
}

type FixtureExtractionEngine struct{}

func (FixtureExtractionEngine) extract(ctx context.Context, _ ExtractionInput, _ ExtractionLimits) (ExtractedRecipe, error) {
	if ctx.Err() != nil {
		return ExtractedRecipe{}, ErrExtractionUnavailable
	}
	servings, prep, cook, total := 4, 10, 15, 25
	quantity := domain.Decimal("300")
	unit := "g"
	return ExtractedRecipe{Title: "Carrot and couscous supper", Description: "A simple recipe for the private import preview.", Servings: &servings, SourceYield: []string{"4 servings"}, PrepMinutes: &prep, CookMinutes: &cook, TotalMinutes: &total, Ingredients: []ExtractedIngredient{{Name: "Carrots", Quantity: &quantity, Unit: &unit, SourceText: "300 g carrots"}, {Name: "Couscous", SourceText: "Couscous, quantity unclear"}}, Instructions: []string{"Slice the carrots and cook until tender.", "Prepare the couscous, then combine and serve."}, Tips: []string{}, Allergens: []string{"Wheat"}, AllergenNote: "Check the couscous label.", Uncertain: []DraftIssue{{Field: "ingredients.1.quantity", Reason: "ambiguous", SourceText: "Couscous, quantity unclear"}}}, nil
}

type FixturePageFetcher struct{}

func (FixturePageFetcher) Fetch(ctx context.Context, address string) (RecipePage, error) {
	if ctx.Err() != nil {
		return RecipePage{}, ErrRecipeSourceUnavailable
	}
	var html string
	switch address {
	case "https://recipes.example.test/carrot-soup":
		html = `<main><h1>Carrot soup</h1><p>A simple written recipe.</p></main><script type="application/ld+json">{"@type":"Recipe","name":"Carrot soup","description":"A simple soup for four.","recipeYield":"4 servings","prepTime":"PT10M","cookTime":"PT20M","totalTime":"PT30M","recipeIngredient":["300 g carrots","500 ml vegetable stock"],"recipeInstructions":["Slice the carrots.","Simmer in the stock until tender, then blend."],"image":"https://source-images.example.test/never-copy.jpg"}</script>`
	case "https://recipes.example.test/plain":
		html = `<main><h1>Carrot and couscous supper</h1><p>Serves four. Slice 300 g carrots and cook until tender. Prepare couscous and combine.</p></main><script>private tracker content</script>`
	case "https://recipes.example.test/multiple":
		html = `<script type="application/ld+json">[{"@type":"Recipe","name":"Carrot soup","recipeIngredient":["2 carrots"],"recipeInstructions":["Simmer gently."]},{"@type":"Recipe","name":"Couscous supper","recipeIngredient":["200 g couscous"],"recipeInstructions":["Prepare the couscous."]}]</script>`
	default:
		return RecipePage{}, ErrRecipeSourceUnavailable
	}
	return RecipePage{URL: address, HTML: []byte(html)}, nil
}
