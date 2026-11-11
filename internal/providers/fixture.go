package providers

import (
	"context"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
)

// Fixture is available only through explicit loopback E2E mode. Reserved regions
// allow browser tests to exercise failures through the ordinary request contract.
type Fixture struct{}

func (Fixture) Generate(ctx context.Context, r domain.GenerationRequest) ([]domain.RecipeContent, error) {
	switch r.Region {
	case "__quota":
		return nil, Failure("provider_quota")
	case "__timeout":
		return nil, Failure("provider_timeout")
	case "__slow":
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	recipes := make([]domain.RecipeContent, len(r.Dates))
	for i := range recipes {
		name := "Carrot"
		if i%2 == 1 {
			name = "Leek"
		}
		q := domain.Decimal("0.12345678901234567890123456789")
		unit := "g"
		recipes[i] = domain.RecipeContent{Title: "Lemon garden pasta", Note: "A simple dinner with bright lemon and seasonal vegetables.", Yield: []string{"2 servings"}, Equipment: "A saucepan and a frying pan", Instructions: []string{"Simmer the vegetables gently for ten minutes.", "Combine with the pasta and lemon, then serve."}, Tips: []string{"Keep a little cooking water to loosen the sauce."}, Allergens: []string{}, AllergenNote: "Check ingredient labels and cross-contamination warnings before cooking.", PrepMinutes: 5, CookMinutes: 10, TotalMinutes: 15, Ingredients: []domain.IngredientContent{{Name: name, Quantity: &q, Unit: &unit}}}
	}
	if r.Region == "__invalid" {
		recipes[0].Instructions = nil
	}
	return recipes, nil
}
func (Fixture) Image(context.Context, domain.RecipeContent) (ImageResult, error) {
	return ImageResult{}, Failure("provider_quota")
}
