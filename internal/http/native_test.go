package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LowGok/my-chef/internal/auth"
)

func callNative(h http.Handler, method, path, body, access string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/native/v1"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if access != "" {
		r.Header.Set("Authorization", "Bearer "+access)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func nativeReply(t *testing.T, w *httptest.ResponseRecorder, status int) auth.NativeTokens {
	t.Helper()
	if w.Code != status {
		t.Fatalf("native response status: got %d, want %d", w.Code, status)
	}
	if w.Header().Get("Set-Cookie") != "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("native credentials mixed with cookies or cacheable response")
	}
	var body struct {
		Native auth.NativeTokens `json:"native"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Native.AccessToken) != 64 || len(body.Native.RefreshToken) != 64 {
		t.Fatal("missing native credential response")
	}
	return body.Native
}

func nativeFixtureLogin(t *testing.T, a *API, h http.Handler) auth.NativeTokens {
	t.Helper()
	_, err := a.Auth.Register(t.Context(), auth.Registration{Email: "native-http@example.test", Password: "Garden native 2026!", Username: "native-http", FirstName: "Native", LastName: "Test"})
	if err != nil {
		t.Fatal(err)
	}
	anonymous := nativeReply(t, callNative(h, "POST", "/session", `{"platform":"ios"}`, ""), 201)
	signed := nativeReply(t, callNative(h, "POST", "/login", `{"email":"native-http@example.test","password":"Garden native 2026!"}`, anonymous.AccessToken), 200)
	if callNative(h, "POST", "/refresh", `{"refreshToken":"`+anonymous.RefreshToken+`"}`, "").Code != 401 {
		t.Fatal("anonymous family survived sign-in")
	}
	return signed
}

func TestNativeHTTPAnonymousLoginRefreshProfileAndLogout(t *testing.T) {
	a, h := testAPI(t)
	tokens := nativeFixtureLogin(t, a, h)
	w := callNative(h, "GET", "/session", "", tokens.AccessToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"firstName":"Native"`) || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("native account session missing or cookie based")
	}
	w = callNative(h, "GET", "/profile", "", tokens.AccessToken)
	if w.Code != 200 || w.Header().Get("X-MyChef-User") == "" {
		t.Fatal("native profile not owned by its live account")
	}
	update := `{"username":"native-http","firstName":"Native Updated","lastName":"Test","birthdate":null,"sex":null,"preferences":{"diet":[],"allergens":[],"dislikes":[],"technique":[],"effort":"Quick weekday meals"}}`
	w = callNative(h, "PUT", "/profile", update, tokens.AccessToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Native Updated") {
		t.Fatal("native mutation required browser Origin/CSRF")
	}
	next := nativeReply(t, callNative(h, "POST", "/refresh", `{"refreshToken":"`+tokens.RefreshToken+`"}`, ""), 200)
	if callNative(h, "GET", "/profile", "", tokens.AccessToken).Code != 401 {
		t.Fatal("rotated native access remained active")
	}
	if callNative(h, "GET", "/profile", "", next.AccessToken).Code != 200 {
		t.Fatal("rotated native access missing")
	}
	w = callNative(h, "POST", "/logout", `{}`, next.AccessToken)
	if w.Code != 204 || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("native logout failed or wrote browser cookie")
	}
	if callNative(h, "POST", "/refresh", `{"refreshToken":"`+next.RefreshToken+`"}`, "").Code != 401 {
		t.Fatal("native refresh survived logout")
	}
}

func TestNativeHTTPTransportCannotSubstituteBrowserAuthority(t *testing.T) {
	a, h := testAPI(t)
	tokens := nativeFixtureLogin(t, a, h)
	current, err := a.Auth.NativeLoad(t.Context(), tokens.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := a.Auth.Rotate(t.Context(), current.Session.UserID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", cookie, tokens.RefreshToken, "malformed"} {
		if callNative(h, "GET", "/profile", "", token).Code != 401 {
			t.Fatal("non-access authority accepted by native guard")
		}
	}
	if call(h, "GET", "/api/v1/profile", "", tokens.AccessToken, "", "").Code != 401 {
		t.Fatal("native credential accepted as browser cookie")
	}
	r := httptest.NewRequest("GET", "/api/native/v1/profile", nil)
	r.AddCookie(&http.Cookie{Name: "mychef_session", Value: cookie})
	r.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("mixed browser/native authority accepted")
	}
	r = httptest.NewRequest("GET", "/api/v1/profile", nil)
	r.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	r.Header.Set("X-MyChef-Native", "ios")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("client header changed browser session transport")
	}
	for _, body := range []string{`{"platform":"web"}`, `{"platform":"ios","userId":"1"}`} {
		if callNative(h, "POST", "/session", body, "").Code != 400 {
			t.Fatal("invalid installation request accepted")
		}
	}
	if call(h, "POST", "/api/v1/session", `{"platform":"ios"}`, "", "", "").Code != 404 {
		t.Fatal("native bootstrap exposed in browser transport")
	}
	if call(h, "PUT", "/api/v1/profile", `{}`, cookie, "", "https://evil.test").Code != 403 {
		t.Fatal("native changes weakened browser CSRF")
	}
}

func TestNativeHTTPReplayRevokesItsInstallationOnly(t *testing.T) {
	a, h := testAPI(t)
	tokens := nativeFixtureLogin(t, a, h)
	other := nativeReply(t, callNative(h, "POST", "/session", `{"platform":"android"}`, ""), 201)
	next := nativeReply(t, callNative(h, "POST", "/refresh", `{"refreshToken":"`+tokens.RefreshToken+`"}`, ""), 200)
	if callNative(h, "POST", "/refresh", `{"refreshToken":"`+tokens.RefreshToken+`"}`, "").Code != 401 {
		t.Fatal("refresh replay accepted")
	}
	if callNative(h, "GET", "/profile", "", next.AccessToken).Code != 401 {
		t.Fatal("replay failed to revoke new access")
	}
	if callNative(h, "GET", "/session", "", other.AccessToken).Code != 200 {
		t.Fatal("replay revoked an unrelated installation")
	}
}

func TestNativeHTTPDoesNotAliasInfrastructureRoutes(t *testing.T) {
	a, _ := testAPI(t)
	called := false
	private := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	a.Config.EmailWebhook, a.Config.FixtureInbox, a.Config.PersonalFixture = private, private, private
	h := a.Handler(nil)
	for _, path := range []string{"/webhooks/email", "/fixture/inbox", "/fixture/personal-recipes/entitlement"} {
		if callNative(h, "POST", path, `{}`, "").Code != 404 || called {
			t.Fatal("native transport exposed a separate infrastructure route")
		}
	}
}
