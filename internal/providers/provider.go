package providers

import (
	"context"

	"github.com/LowGok/my-chef/internal/domain"
)

type Generator interface {
	Generate(context.Context, domain.GenerationRequest) ([]domain.RecipeContent, error)
}
type Images interface {
	Image(context.Context, domain.RecipeContent) (ImageResult, error)
}
type ImageResult struct {
	URL      string
	PublicID string
}
type Error struct{ Kind string }

func (e *Error) Error() string  { return e.Kind }
func (e *Error) Code() string   { return e.Kind }
func Failure(code string) error { return &Error{Kind: code} }
func PublicMessage(code string) string {
	switch code {
	case "menu_exists":
		return "Meals already exist for this week. Review your request to replace them."
	case "provider_quota":
		return "The recipe provider has no quota available. Try again when its quota is restored."
	case "provider_timeout":
		return "The recipe provider timed out. Check this request before explicitly starting a new one."
	case "provider_invalid":
		return "The provider returned recipes that did not meet this request. Review your preferences before trying a new menu."
	case "storage_unavailable":
		return "The menu could not be saved. No partial menu was kept. Try again when storage is available."
	case "interrupted":
		return "The server restarted or stopped during this request. Start a new request explicitly if you still need a menu."
	default:
		return "The recipe provider is unavailable. Please try again later."
	}
}
