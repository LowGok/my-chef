package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/menus"
	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func TestPlanningWindowAppliesToWebAndNativeDirectIDs(t *testing.T) {
	a, _, webUser, token := lifecycleHTTPFixture(t)
	now := time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC)
	a.Config.Planning = &store.PlanningPolicy{Now: func() time.Time { return now },
		HouseholdZone: func(context.Context, *gorm.DB, int64) (string, error) { return "Europe/Brussels", nil }}
	h := a.Handler(nil)
	native := nativeFixtureLogin(t, a, h)
	var nativeUser int64
	if err := a.Store.SQL.QueryRow(`SELECT user_id FROM users WHERE email='native-http@example.test'`).Scan(&nativeUser); err != nil {
		t.Fatal(err)
	}
	webGet := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1"+path, nil)
		r.AddCookie(&http.Cookie{Name: "mychef_session", Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, client := range []struct {
		name string
		id   int64
		get  func(string) *httptest.ResponseRecorder
	}{
		{"web", webUser.ID, webGet}, {"native", nativeUser, func(path string) *httptest.ResponseRecorder {
			return callNative(h, "GET", path, "", native.AccessToken)
		}},
	} {
		t.Run(client.name, func(t *testing.T) {
			for _, sample := range []struct {
				day    string
				status int
			}{{"2026-11-16", http.StatusOK}, {"2026-11-23", http.StatusNotFound}} {
				menu := store.MenuRecord{UserID: client.id, CreatedUnix: now.Unix()}
				if err := a.Store.DB.Create(&menu).Error; err != nil {
					t.Fatal(err)
				}
				meal := store.Meal{CreatorID: client.id, MenuID: &menu.ID, CreationDate: sample.day, RecipeTitle: "Planning fixture " + sample.day, ImageStatus: "ready"}
				if err := a.Store.DB.Create(&meal).Error; err != nil {
					t.Fatal(err)
				}
				gen := store.Generation{UserID: client.id, Key: "fixture-" + sample.day, RequestHash: "fixture", RequestJSON: fmt.Sprintf(`{"dates":[%q]}`, sample.day), Status: "succeeded", Stage: "finished", MenuID: &menu.ID}
				if err := a.Store.DB.Create(&gen).Error; err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{fmt.Sprintf("/menus/%d", menu.ID), fmt.Sprintf("/recipes/%d", meal.ID), fmt.Sprintf("/generations/%d", gen.ID), "/shopping/" + sample.day, "/favorite-weeks/" + sample.day} {
					if w := client.get(path); w.Code != sample.status {
						t.Errorf("%s returned %d, want %d (%s)", path, w.Code, sample.status, w.Body.String())
					}
				}
			}
			w := client.get("/menus?from=2026-11-16&to=2026-11-30")
			if w.Code != 200 || strings.Contains(w.Body.String(), "Planning fixture 2026-11-23") {
				t.Fatal("history leaked a future meal", w.Code)
			}
		})
	}
}

func TestPlanningCorrectionRequiresOwnedMealOnBothTransports(t *testing.T) {
	a, _, webUser, token := lifecycleHTTPFixture(t)
	now := time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC)
	a.Config.Planning = &store.PlanningPolicy{Now: func() time.Time { return now },
		HouseholdZone: func(context.Context, *gorm.DB, int64) (string, error) { return "Europe/Brussels", nil }}
	worker, err := menus.New(a.Store, providers.Fixture{}, nil, menus.Options{Planning: a.Config.Planning})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(worker.Close)
	a.Generator = worker
	h := a.Handler(nil)
	native := nativeFixtureLogin(t, a, h)
	meal := store.Meal{CreatorID: webUser.ID, CreationDate: "2026-11-20", RecipeTitle: "A recipe missing its instructions", ImageStatus: "ready"}
	if err := a.Store.DB.Create(&meal).Error; err != nil {
		t.Fatal(err)
	}
	post := func(transport, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", fmt.Sprintf("/api/%s/recipes/%d/correction", transport, meal.ID), strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "correction-check-key")
		if transport == "native/v1" {
			r.Header.Set("Authorization", "Bearer "+native.AccessToken)
		} else {
			r.AddCookie(&http.Cookie{Name: "mychef_session", Value: token})
			r.Header.Set("Origin", a.Config.Origin)
			r.Header.Set("X-CSRF-Token", auth.CSRF(token))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := post("native/v1", "{}"); w.Code != 404 {
		t.Fatal("a native session could address someone else's dinner", w.Code, w.Body.String())
	}
	if w := post("v1", `{"freeCorrection":true}`); w.Code != 400 {
		t.Fatal("a client could submit its own exemption", w.Code, w.Body.String())
	}
	if w := post("v1", "{}"); w.Code != 202 {
		t.Fatal("the owner could not request a verifiable correction", w.Code, w.Body.String())
	}
}

func TestPlanningZoneOnlyChangesOnExplicitConfirmation(t *testing.T) {
	a, _, web, token := lifecycleHTTPFixture(t)
	now := time.Date(2026, 11, 15, 23, 30, 0, 0, time.UTC)
	a.Config.Planning = &store.PlanningPolicy{Now: func() time.Time { return now }, HouseholdZone: store.ConfirmedPlanningZone}
	h := a.Handler(nil)
	webCall := func(method, path, body string) *httptest.ResponseRecorder {
		return call(h, method, "/api/v1"+path, body, token, auth.CSRF(token), a.Config.Origin)
	}
	if w := webCall("GET", "/planning", ""); w.Code != 200 || strings.Contains(w.Body.String(), "2026-11") {
		t.Fatalf("an unconfirmed account received a planning week: %d %s", w.Code, w.Body)
	}
	if w := webCall("PUT", "/planning/zone", `{"zone":"Local"}`); w.Code != 400 {
		t.Fatalf("a device-local time zone was accepted: %d", w.Code)
	}
	if w := webCall("PUT", "/planning/zone", `{"zone":"Europe/Brussels","premium":true}`); w.Code != 400 {
		t.Fatalf("caller could supply their own tier: %d", w.Code)
	}
	if w := webCall("PUT", "/planning/zone", `{"zone":"Europe/Brussels"}`); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"firstWeek":"2026-11-16"`) ||
		!strings.Contains(w.Body.String(), `"lastWeek":"2026-11-16"`) ||
		!strings.Contains(w.Body.String(), `"premium":false`) {
		t.Fatalf("Brussels Monday was not the Free planning week: %d %s", w.Code, w.Body)
	}
	body := fmt.Sprintf(`{"username":%q,"firstName":"Edited","lastName":"Fixture","preferences":{"diet":["vegetarian"],"allergens":[],"dislikes":[],"technique":[],"effort":"Quick"}}`, web.Username)
	if w := webCall("PUT", "/profile", body); w.Code != 200 {
		t.Fatalf("saving ordinary preferences failed: %d %s", w.Code, w.Body)
	}
	if w := webCall("GET", "/planning", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"zone":"Europe/Brussels"`) {
		t.Fatalf("ordinary preference save erased the confirmed time zone: %d %s", w.Code, w.Body)
	}

	native := nativeFixtureLogin(t, a, h)
	if w := callNative(h, "GET", "/planning", "", native.AccessToken); w.Code != 200 || strings.Contains(w.Body.String(), "Brussels") {
		t.Fatalf("planning zone leaked to another account: %d %s", w.Code, w.Body)
	}
	if w := callNative(h, "PUT", "/planning/zone", `{"zone":"America/New_York"}`, native.AccessToken); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"firstWeek":"2026-11-09"`) {
		t.Fatalf("native zone did not establish the local week: %d %s", w.Code, w.Body)
	}
}

func TestAlternateRecipeQueuesUseHouseholdHorizon(t *testing.T) {
	a, _, owner, token, _, _, _, _ := personalHTTPFixture(t)
	var premium atomic.Bool
	premium.Store(true)
	reader := func(_ context.Context, _ *gorm.DB, user int64, _ string) (store.PersonalEntitlement, error) {
		return store.PersonalEntitlement{Premium: user == owner && premium.Load(), ExpiresUnix: time.Now().Add(time.Hour).Unix()}, nil
	}
	now := time.Date(2026, 11, 15, 23, 30, 0, 0, time.UTC)
	a.Config.PersonalEntitlements = reader
	a.Config.Planning = &store.PlanningPolicy{Now: func() time.Time { return now },
		HouseholdZone: func(context.Context, *gorm.DB, int64) (string, error) { return "Europe/Brussels", nil },
		Entitlements:  reader}
	h := a.Handler(nil)
	web := func(method, path, body string) *httptest.ResponseRecorder {
		return call(h, method, "/api/v1"+path, body, token, auth.CSRF(token), a.Config.Origin)
	}
	meal := store.Meal{CreatorID: owner, CreationDate: "2026-11-16", RecipeTitle: "A saved dinner", ImageStatus: "unavailable"}
	if err := a.Store.DB.Create(&meal).Error; err != nil {
		t.Fatal(err)
	}
	if w := web("PUT", fmt.Sprintf("/recipes/%d/favorite", meal.ID), "{}"); w.Code != 200 {
		t.Fatal("favorite setup failed", w.Code)
	}
	recipe := readHTTPPersonal(t, web("POST", "/personal-recipes", personalRequestBody(t, strings.Repeat("1", 32), httpPersonalDocument(), "")), 201)
	anchor, err := a.Auth.Rotate(t.Context(), &owner, "")
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := a.Auth.NativeBind(t.Context(), anchor, "ios", "")
	if err != nil {
		t.Fatal(err)
	}
	queue := func(week, today, key string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"week":%q,"today":%q,"versionId":%q,"requestKey":%q}`, week, today, recipe.VersionID, key)
		return callNative(h, "POST", "/personal-recipes/"+recipe.ID+"/queue", body, tokens.AccessToken)
	}
	fav := func(week, today string) *httptest.ResponseRecorder {
		return web("POST", fmt.Sprintf("/favorites/%d/queue", meal.ID), fmt.Sprintf(`{"week":%q,"today":%q}`, week, today))
	}
	last := "2026-12-14"
	valid := queue(last, "1999-01-01", strings.Repeat("2", 32))
	var out struct{ Entries []struct{ ID string } }
	if valid.Code != 200 || json.Unmarshal(valid.Body.Bytes(), &out) != nil || len(out.Entries) != 1 {
		t.Fatalf("native queue did not use the household's fourth future week: %d %s", valid.Code, valid.Body)
	}
	if w := fav(last, "1999-01-01"); w.Code != 200 {
		t.Fatalf("web favorite queue did not use the server calendar: %d %s", w.Code, w.Body)
	}
	if w := queue("2026-12-21", "2026-12-14", strings.Repeat("3", 32)); w.Code != 404 {
		t.Fatalf("native allowed a fifth future week: %d %s", w.Code, w.Body)
	}
	if w := fav("2026-11-16", "2026-11-09"); w.Code != 409 {
		t.Fatalf("web allowed a new future assignment in the current week: %d %s", w.Code, w.Body)
	}
	premium.Store(false)
	if w := fav("2026-11-23", "2026-11-16"); w.Code != 409 {
		t.Fatalf("a downgraded account queued a generated favorite: %d %s", w.Code, w.Body)
	}
	remove := "/personal-queue/" + out.Entries[0].ID
	key := fmt.Sprintf(`{"requestKey":%q}`, strings.Repeat("4", 32))
	if w := callNative(h, "DELETE", remove, key, tokens.AccessToken); w.Code != 404 {
		t.Fatalf("a known ID exposed or changed a hidden queue: %d %s", w.Code, w.Body)
	}
	now = time.Date(2026, 12, 14, 12, 0, 0, 0, time.UTC)
	if w := callNative(h, "DELETE", remove, key, tokens.AccessToken); w.Code != 200 {
		t.Fatalf("a retained assignment could not be removed once its week arrived: %d %s", w.Code, w.Body)
	}
}
