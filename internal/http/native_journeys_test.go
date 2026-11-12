package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/imports"
	"github.com/LowGok/my-chef/internal/menus"
	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
)

type nativeJourneyProvider struct{ calls atomic.Int64 }

func (p *nativeJourneyProvider) Generate(ctx context.Context, input domain.GenerationRequest) ([]domain.RecipeContent, error) {
	p.calls.Add(1)
	return (providers.Fixture{}).Generate(ctx, input)
}

type nativeRevokeBody struct {
	*strings.Reader
	once sync.Once
	hook func()
}

func (b *nativeRevokeBody) Read(value []byte) (int, error) {
	b.once.Do(b.hook)
	return b.Reader.Read(value)
}

func nativeJourneySession(t *testing.T, a *API, cookie, platform string) auth.NativeTokens {
	t.Helper()
	tokens, err := a.Auth.NativeBind(t.Context(), cookie, platform, "")
	if err != nil {
		t.Fatal(err)
	}
	return tokens
}

func TestNativeJourneyRevocationDuringBodyReadCannotCommitMutations(t *testing.T) {
	for _, kind := range []string{"profile", "shopping", "favorite queue", "planning"} {
		t.Run(kind, func(t *testing.T) {
			a, _ := testAPI(t)
			owner, cookie := feedbackSession(t, a, "native-race", true)
			a.Config.Verification = &auth.EmailVerification{}
			tokens := nativeJourneySession(t, a, cookie, "ios")
			current, err := a.Auth.NativeLoad(t.Context(), tokens.AccessToken)
			if err != nil {
				t.Fatal(err)
			}
			provider := &nativeJourneyProvider{}
			method, path, body := "PUT", "/profile", `{"username":"native-race","firstName":"Unauthorized edit","lastName":"Cook","birthdate":null,"sex":null,"preferences":{"diet":[],"allergens":[],"dislikes":[],"technique":[],"effort":"Quick"}}`
			if kind == "planning" {
				service, err := menus.New(a.Store, provider, nil, menus.Options{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(service.Close)
				a.Generator = service
				method, path, body = "POST", "/generations", `{"dates":["2026-12-31"],"adults":2,"children":0,"maxMinutes":30,"budget":"low","region":"Belgium","creativity":5}`
			}
			if kind == "shopping" || kind == "favorite queue" {
				meal := store.Meal{CreatorID: owner, CreationDate: "2026-11-02", RecipeTitle: "Native carrots", ImageStatus: "unavailable"}
				if err := a.Store.DB.Create(&meal).Error; err != nil {
					t.Fatal(err)
				}
				if err := a.Store.DB.Create(&store.Ingredient{RecipeID: meal.ID, Name: "Carrot", Quantity: store.Ptr("2")}).Error; err != nil {
					t.Fatal(err)
				}
				if kind == "shopping" {
					list, err := a.Store.Shopping(t.Context(), owner, "2026-11-02")
					if err != nil || len(list.Items) != 1 {
						t.Fatal("shopping fixture unavailable")
					}
					list.Items[0].State = "basket"
					data, err := json.Marshal(list)
					if err != nil {
						t.Fatal(err)
					}
					path, body = "/shopping/2026-11-02", string(data)
				} else {
					if _, err := a.Store.SetFavorite(t.Context(), owner, meal.ID, true); err != nil {
						t.Fatal(err)
					}
					now := time.Now().UTC()
					next := now.AddDate(0, 0, 7-((int(now.Weekday())+6)%7)).Format(time.DateOnly)
					method, path, body = "POST", fmt.Sprintf("/favorites/%d/queue", meal.ID), fmt.Sprintf(`{"week":%q,"today":%q}`, next, now.Format(time.DateOnly))
				}
			}
			h := a.Handler(nil)
			reader := &nativeRevokeBody{Reader: strings.NewReader(body), hook: func() {
				if err := a.Auth.NativeRevoke(t.Context(), current.FamilyID); err != nil {
					t.Fatal(err)
				}
			}}
			r := httptest.NewRequest(method, "/api/native/v1"+path, reader)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
			r.Header.Set("Idempotency-Key", "native-session-race")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatalf("revoked native actor committed %s mutation: status %d", kind, w.Code)
			}
			if provider.calls.Load() != 0 {
				t.Fatal("revoked planning request reached provider")
			}
		})
	}
}

func TestNativeJourneyPersonalPhotoOwnershipAndLateRevocation(t *testing.T) {
	a, h, _, cookie, otherCookie, _, _, _ := personalHTTPFixture(t)
	tokens := nativeJourneySession(t, a, cookie, "ios")
	other := nativeJourneySession(t, a, otherCookie, "android")
	var raw bytes.Buffer
	if err := png.Encode(&raw, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	metadata := strings.Replace(personalRequestBody(t, strings.Repeat("a", 32), httpPersonalDocument(), ""), `"keep"`, `"replace"`, 1)
	body, media := photoHTTPBody(t, metadata, raw.Bytes())
	save := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/native/v1/personal-recipes", bytes.NewReader(body))
		r.Header.Set("Content-Type", media)
		r.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	recipe := readHTTPPersonal(t, save(), 201)
	replay := readHTTPPersonal(t, save(), 201)
	if recipe.PhotoID == nil || recipe.ImageURL == nil || replay.ID != recipe.ID || replay.VersionID != recipe.VersionID {
		t.Fatal("native photo retry lost the saved image or duplicated recipe")
	}
	path := strings.TrimPrefix(*recipe.ImageURL, "/api/v1")
	if w := callNative(h, "GET", path, "", tokens.AccessToken); w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("Location") != "" {
		t.Fatal("native private photo was unavailable or redirected")
	}
	if callNative(h, "GET", path, "", other.AccessToken).Code != 404 {
		t.Fatal("native photo crossed account boundary")
	}
	current, err := a.Auth.NativeLoad(t.Context(), tokens.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	a.Config.PersonalPhotos = interceptedPersonalPhotos{reader: a.Config.PersonalPhotos, after: func(data []byte) []byte {
		if err := a.Auth.NativeRevoke(t.Context(), current.FamilyID); err != nil {
			t.Fatal(err)
		}
		return data
	}}
	if callNative(a.Handler(nil), "GET", path, "", tokens.AccessToken).Code != 401 {
		t.Fatal("native revocation during photo read delivered private bytes")
	}
}

func TestNativeJourneyImportsPreservePremiumReviewSourceAndOwnership(t *testing.T) {
	a, h, cookie, otherCookie, _, worker := importHTTPFixture(t)
	tokens := nativeJourneySession(t, a, cookie, "ios")
	other := nativeJourneySession(t, a, otherCookie, "android")
	body := `{"requestKey":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","url":"https://recipes.example.com/soup","personalUseConfirmed":true}`
	if callNative(h, "POST", "/recipe-imports/url", body, other.AccessToken).Code != 403 || worker.pages.Load() != 0 {
		t.Fatal("native platform granted Premium or started unauthorized source work")
	}
	op := readImportHTTP(t, callNative(h, "POST", "/recipe-imports/url", body, tokens.AccessToken), 202)
	path := "/recipe-imports/" + op.ID
	for range 100 {
		op = readImportHTTP(t, callNative(h, "GET", path, "", tokens.AccessToken), 200)
		if op.Stage == "ready" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if op.Draft == nil || callNative(h, "GET", path, "", other.AccessToken).Code != 404 {
		t.Fatal("native draft unavailable or visible to another account")
	}
	request, err := json.Marshal(map[string]any{"content": op.Draft.Content})
	if err != nil {
		t.Fatal(err)
	}
	w := callNative(h, "POST", path+"/review", string(request), tokens.AccessToken)
	var review imports.ReviewSnapshot
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &review) != nil {
		t.Fatal("native review unavailable")
	}
	resolved := []string{}
	for _, issue := range op.Draft.Issues {
		resolved = append(resolved, issue.Field)
	}
	confirmation := imports.ReviewConfirmation{DraftRevision: op.Draft.Revision, ContentRevision: review.ContentRevision, ReviewedFields: review.RequiredFields, ResolvedFields: resolved}
	request, err = json.Marshal(map[string]any{"requestKey": strings.Repeat("f", 32), "content": review.Content, "photoAction": "keep", "import": map[string]any{"operationId": op.ID, "confirmation": confirmation}})
	if err != nil {
		t.Fatal(err)
	}
	saved := readHTTPPersonal(t, callNative(h, "POST", "/personal-recipes", string(request), tokens.AccessToken), 201)
	if saved.ImportSource == nil || saved.ImportSource.URL != "https://recipes.example.com/soup" || saved.ImageURL != nil || worker.extractions.Load() != 0 {
		t.Fatal("native import changed provenance, fallback or provider consent")
	}
}

func TestNativeJourneyPlanningFavoritesShoppingAndFeedback(t *testing.T) {
	a, _ := testAPI(t)
	_, cookie := feedbackSession(t, a, "native-core", true)
	a.Config.Verification = &auth.EmailVerification{}
	a.Config.FeedbackTo = "operator@example.test"
	provider := &nativeJourneyProvider{}
	service, err := menus.New(a.Store, provider, nil, menus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	a.Generator = service
	h := a.Handler(nil)
	tokens := nativeJourneySession(t, a, cookie, "android")
	body := `{"dates":["2026-12-31"],"adults":2,"children":0,"maxMinutes":30,"budget":"low","region":"Belgium","creativity":5}`
	start := func() domain.Operation {
		r := httptest.NewRequest("POST", "/api/native/v1/generations", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
		r.Header.Set("Idempotency-Key", "native-core-planning")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var operation domain.Operation
		if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &operation) != nil || operation.ID == "" {
			t.Fatal("native planning did not start")
		}
		return operation
	}
	op := start()
	for range 100 {
		w := callNative(h, "GET", "/generations/"+op.ID, "", tokens.AccessToken)
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &op) != nil {
			t.Fatal("native operation could not be read")
		}
		if op.Status == "succeeded" || op.Status == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if op.Status != "succeeded" || op.MenuID == nil || start().ID != op.ID || provider.calls.Load() != 1 {
		t.Fatal("native planning failed or exact retry repeated provider work")
	}
	w := callNative(h, "GET", "/menus/"+*op.MenuID, "", tokens.AccessToken)
	var menu domain.Menu
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &menu) != nil || len(menu.Recipes) != 1 {
		t.Fatal("native planned menu was not readable")
	}
	recipe := menu.Recipes[0]
	for _, path := range []string{"/recipes/" + recipe.ID, "/menus?from=2026-12-28&to=2027-01-04"} {
		if callNative(h, "GET", path, "", tokens.AccessToken).Code != 200 {
			t.Fatal("native saved recipe/history unavailable")
		}
	}
	if callNative(h, "PUT", "/recipes/"+recipe.ID+"/favorite", `{}`, tokens.AccessToken).Code != 200 {
		t.Fatal("native favorite save failed")
	}
	w = callNative(h, "GET", "/favorites", "", tokens.AccessToken)
	var favorites domain.RecipePage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &favorites) != nil || len(favorites.Recipes) != 1 {
		t.Fatal("native favorites list unavailable")
	}
	w = callNative(h, "GET", "/shopping/2026-12-28", "", tokens.AccessToken)
	var list domain.ShoppingList
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Items) != 1 {
		t.Fatal("native shopping source unavailable")
	}
	list.Items[0].State = "basket"
	update, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if callNative(h, "PUT", "/shopping/2026-12-28", string(update), tokens.AccessToken).Code != 200 {
		t.Fatal("native shopping mutation failed")
	}
	feedback := `{"requestKey":"11111111111111111111111111111111","topic":"technical_issue","message":"Synthetic native feedback example."}`
	first := callNative(h, "POST", "/feedback", feedback, tokens.AccessToken)
	replay := callNative(h, "POST", "/feedback", feedback, tokens.AccessToken)
	if first.Code != 200 || replay.Code != 200 || first.Body.String() != replay.Body.String() {
		t.Fatal("native feedback receipt or exact retry failed")
	}
	rating := `{"requestKey":"22222222222222222222222222222222","rating":4}`
	if callNative(h, "PUT", "/recipes/"+recipe.ID+"/rating", rating, tokens.AccessToken).Code != 200 {
		t.Fatal("native private recipe rating failed")
	}
}
