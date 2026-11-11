package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/store"
)

func testAPI(t *testing.T) (*API, http.Handler) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "http.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := New(s, nil, Config{Origin: "https://mychef.test", Secure: true, Version: "test"}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	return a, a.Handler(nil)
}
func call(h http.Handler, method, path, body, token, csrf, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "mychef_session", Value: token})
	}
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == "mychef_session" {
			if !c.HttpOnly || !c.Secure || c.Domain != "" || c.SameSite != http.SameSiteLaxMode {
				t.Fatal("unsafe cookie flags")
			}
			return c.Value
		}
	}
	t.Fatal("missing session cookie")
	return ""
}

func TestEmailReadiness(t *testing.T) {
	a, _ := testAPI(t)
	ready := true
	a.Config.EmailReady = func(context.Context) bool { return ready }
	h := a.Handler(nil)
	if code := call(h, "GET", "/readyz", "", "", "", "").Code; code != 200 {
		t.Fatalf("healthy worker: %d", code)
	}
	ready = false
	if code := call(h, "GET", "/readyz", "", "", "", "").Code; code != 503 {
		t.Fatalf("failed worker: %d", code)
	}
	if code := call(h, "GET", "/healthz", "", "", "", "").Code; code != 200 {
		t.Fatalf("liveness after worker failure: %d", code)
	}
}

func TestHTTPAuthenticationCSRFAndRotation(t *testing.T) {
	a, h := testAPI(t)
	w := call(h, "GET", "/api/v1/session", "", "", "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	anonymous := sessionCookie(t, w)
	payload := `{"email":"ada@example.test","password":"ChefTest2026!","username":"ada","firstName":"Ada","lastName":"Chef"}`
	for _, origin := range []string{"", "https://evil.test"} {
		w = call(h, "POST", "/api/v1/register", payload, anonymous, auth.CSRF(anonymous), origin)
		if w.Code != 403 {
			t.Fatalf("invalid origin: %d", w.Code)
		}
	}
	w = call(h, "POST", "/api/v1/register", payload, anonymous, auth.CSRF(anonymous), a.Config.Origin)
	if w.Code != 201 {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	token := sessionCookie(t, w)
	if token == anonymous {
		t.Fatal("token did not rotate")
	}
	if _, err := a.Auth.Load(context.Background(), anonymous); err == nil {
		t.Fatal("old session remains valid")
	}
	if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "ChefTest2026") {
		t.Fatal("credentials in response")
	}
	w = call(h, "GET", "/api/v1/profile", "", token, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	update := `{"username":"ada","firstName":"Ada Updated","lastName":"Chef","birthdate":null,"sex":null,"preferences":{"diet":["Vegetarian"],"allergens":["Peanuts"],"dislikes":[],"technique":[],"effort":"Quick weekday meals"}}`
	w = call(h, "PUT", "/api/v1/profile", update, token, "bad", a.Config.Origin)
	if w.Code != 403 {
		t.Fatal("bad CSRF accepted")
	}
	w = call(h, "PUT", "/api/v1/profile", update, token, auth.CSRF(token), a.Config.Origin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Ada Updated") {
		t.Fatalf("profile update: %d %s", w.Code, w.Body.String())
	}
	w = call(h, "POST", "/api/v1/logout", "{}", token, auth.CSRF(token), a.Config.Origin)
	if w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	w = call(h, "GET", "/api/v1/profile", "", token, "", "")
	if w.Code != 401 {
		t.Fatal("logged-out token accepted")
	}
}

func TestOwnerIsolationAndSafeErrors(t *testing.T) {
	a, h := testAPI(t)
	ctx := context.Background()
	owner, err := a.Auth.Register(ctx, auth.Registration{Email: "owner@test.invalid", Password: "ChefTest2026!", Username: "owner", FirstName: "Owner", LastName: "Chef"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.Auth.Register(ctx, auth.Registration{Email: "other@test.invalid", Password: "ChefTest2026!", Username: "other", FirstName: "Other", LastName: "Chef"})
	if err != nil {
		t.Fatal(err)
	}
	meal := store.Meal{ID: 9223372036854775000, CreatorID: owner.ID, CreationDate: "2026-12-31", RecipeTitle: "Private meal", ImageStatus: "unavailable"}
	if err = a.Store.DB.Create(&meal).Error; err != nil {
		t.Fatal(err)
	}
	token, err := a.Auth.Rotate(ctx, &other.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	w := call(h, "GET", "/api/v1/recipes/9223372036854775000", "", token, "", "")
	if w.Code != 404 || strings.Contains(w.Body.String(), "Private meal") {
		t.Fatalf("cross-owner read: %d %s", w.Code, w.Body.String())
	}
	w = call(h, "GET", "/api/v1/menus?from=2026-12-28&to=2027-01-04", "", token, "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "Private meal") {
		t.Fatal("history leaked a recipe")
	}
	w = call(h, "PUT", "/api/v1/profile", `{"user_id":1}`, token, auth.CSRF(token), a.Config.Origin)
	if w.Code != 400 {
		t.Fatal("owner injection accepted")
	}
	token, err = a.Auth.Rotate(ctx, &owner.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	w = call(h, "GET", "/api/v1/recipes/9223372036854775000", "", token, "", "")
	var result map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["id"] != "9223372036854775000" {
		t.Fatal("BIGINT did not serialize as text")
	}
	w = call(h, "POST", "/api/v1/login", `{"email":"' OR 1=1--","password":"incorrect"}`, token, auth.CSRF(token), a.Config.Origin)
	if w.Code != 401 || bytes.Contains(w.Body.Bytes(), []byte("SELECT")) {
		t.Fatalf("unsafe credentials error: %d %s", w.Code, w.Body.String())
	}
}

func TestHTTPRegistrationAndFreshLoginWithLongPassword(t *testing.T) {
	a, h := testAPI(t)
	pwd := strings.Repeat("😀", 128)
	registered, err := json.Marshal(auth.Registration{
		Email: "long@example.test", Password: pwd, Username: "long", FirstName: "Long", LastName: "Test",
	})
	if err != nil {
		t.Fatal(err)
	}
	anon := sessionCookie(t, call(h, "GET", "/api/v1/session", "", "", "", ""))
	w := call(h, "POST", "/api/v1/register", string(registered), anon, auth.CSRF(anon), a.Config.Origin)
	if w.Code != 201 {
		t.Fatalf("long registration returned %d", w.Code)
	}
	anon = sessionCookie(t, call(h, "GET", "/api/v1/session", "", "", "", ""))
	login, err := json.Marshal(map[string]string{"email": "long@example.test", "password": pwd})
	if err != nil {
		t.Fatal(err)
	}
	w = call(h, "POST", "/api/v1/login", string(login), anon, auth.CSRF(anon), a.Config.Origin)
	if w.Code != 200 {
		t.Fatalf("fresh long sign-in returned %d", w.Code)
	}
	if sessionCookie(t, w) == anon || strings.Contains(w.Body.String(), pwd) {
		t.Fatal("fresh login did not rotate the session or returned a password")
	}
}

func TestUnavailableLifecycleJournalBlocksAccountRequests(t *testing.T) {
	a, _ := testAPI(t)
	ready := false
	a.Config.AccountReady = func(context.Context) bool { return ready }
	h := a.Handler(nil)
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/session"}, {"POST", "/api/v1/register"}, {"GET", "/api/v1/profile"}} {
		w := call(h, route.method, route.path, "{}", "", "", a.Config.Origin)
		if w.Code != 503 || len(w.Result().Cookies()) != 0 {
			t.Fatal("unavailable lifecycle journal allowed account activity", w.Code)
		}
	}
	var n int64
	if err := a.Store.DB.Model(&store.Session{}).Count(&n).Error; err != nil || n != 0 {
		t.Fatal("blocked request created a session", err)
	}
	ready = true
	if code := call(h, "GET", "/api/v1/session", "", "", "", "").Code; code != 200 {
		t.Fatal("healthy journal blocked session", code)
	}
}
