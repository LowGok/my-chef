package menus

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
)

type privateBoundaryGenerator struct{ request chan domain.GenerationRequest }

func (p *privateBoundaryGenerator) Generate(ctx context.Context, input domain.GenerationRequest) ([]domain.RecipeContent, error) {
	p.request <- input
	return (providers.Fixture{}).Generate(ctx, input)
}

func TestCapturedContentNeverReachesRemainingSlotProvider(t *testing.T) {
	s, user := workerStore(t)
	ctx := t.Context()
	meal := store.Meal{CreatorID: user, CreationDate: "2026-11-02", RecipeTitle: "captured-private-title-canary", Instructions: store.Ptr(`["captured-private-body-canary"]`), ImageStatus: "ready", ImageURL: store.Ptr("/api/v1/personal-photos/captured-private-image-canary")}
	if err := s.DB.Create(&meal).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFavorite(ctx, user, meal.ID, true); err != nil {
		t.Fatal(err)
	}
	week, err := s.QueueFavorite(ctx, user, meal.ID, "2026-12-28")
	if err != nil {
		t.Fatal(err)
	}
	input := request("Belgium")
	input.Dates = append(input.Dates, "2027-01-01")
	input.FavoriteRevisions = map[string]string{week.Week: week.Revision}
	p := &privateBoundaryGenerator{request: make(chan domain.GenerationRequest, 1)}
	service, err := New(s, p, providers.Fixture{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	op, err := service.Start(ctx, user, "private-provider-boundary", input)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(op.ID, 10, 64)
	if result := waitOperation(t, service, user, id); result.Status != "succeeded" {
		t.Fatal(result)
	}
	providerRequest := <-p.request
	encoded, _ := json.Marshal(providerRequest)
	if len(providerRequest.Favorites) != 0 || providerRequest.FavoriteRevisions != nil || len(providerRequest.Dates) != 1 || strings.Contains(string(encoded), "captured-private-") {
		t.Fatal("captured recipe reached provider boundary")
	}
}

type personalPlanningImages struct {
	calls    atomic.Int32
	contents chan domain.RecipeContent
}

func (p *personalPlanningImages) Image(_ context.Context, content domain.RecipeContent) (providers.ImageResult, error) {
	p.calls.Add(1)
	p.contents <- content
	return providers.ImageResult{}, providers.Failure("provider_quota")
}

func TestPersonalPlanningUsesOnlyRemainingProviderSlots(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(strconv.FormatBool(all), func(t *testing.T) {
			s, j, actor, reader, input, raw := personalSaveFixture(t)
			ctx := t.Context()
			now := time.Now()
			input.Content.Title = "personal-provider-title-canary"
			input.Content.Instructions = []string{"personal-provider-instruction-canary"}
			photos := providers.NewFixturePersonalPhotos()
			saves := NewPersonalRecipes(s, j, reader, &countedPhotoNormalizer{}, photos)
			recipe, err := saves.Save(ctx, actor, input, raw, "192.0.2.5")
			if err != nil {
				t.Fatal(err)
			}
			week, err := s.QueuePersonalRecipe(ctx, actor, reader, recipe.ID, recipe.VersionID, "2026-12-28", strings.Repeat("b", 32), now)
			if err != nil {
				t.Fatal(err)
			}
			in := request("Belgium")
			if !all {
				in.Dates = append(in.Dates, "2027-01-01")
			}
			in.FavoriteRevisions = map[string]string{week.Week: week.Revision}
			p := &privateBoundaryGenerator{request: make(chan domain.GenerationRequest, 1)}
			images := &personalPlanningImages{contents: make(chan domain.RecipeContent, 1)}
			service, err := New(s, p, images, Options{Lifecycle: j, PersonalEntitlements: reader, Shuffle: func([]string) {}})
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()
			op, err := service.StartPersonal(ctx, actor, "personal-plan", in)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := strconv.ParseInt(op.ID, 10, 64)
			result := waitOperation(t, service, actor.UserID, id)
			if result.Status != "succeeded" {
				t.Fatal(result)
			}
			menuID, _ := strconv.ParseInt(*result.MenuID, 10, 64)
			menu, err := s.GetMenu(ctx, actor.UserID, menuID)
			if err != nil || len(menu.Recipes) != len(in.Dates) {
				t.Fatal("personal plan missing", err)
			}
			if menu.Recipes[0].Source == nil || menu.Recipes[0].Source.VersionID != recipe.VersionID || menu.Recipes[0].Title != recipe.Content.Title {
				t.Fatal("personal snapshot changed")
			}
			if all {
				if len(p.request) != 0 || images.calls.Load() != 0 {
					t.Fatal("all-personal plan called generation")
				}
			} else {
				providerRequest := <-p.request
				encoded, _ := json.Marshal(providerRequest)
				if len(providerRequest.Dates) != 1 || len(providerRequest.Favorites) != 0 || strings.Contains(string(encoded), "personal-provider-") || strings.Contains(string(encoded), "personal-photos/") {
					t.Fatal("personal content reached generation provider")
				}
				select {
				case content := <-images.contents:
					if strings.Contains(content.Title, "personal-provider-") {
						t.Fatal("personal content reached image provider")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("remaining generated meal image was not attempted")
				}
			}
		})
	}
}
