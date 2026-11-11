package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/store"
)

const passwordPath = "/api/v1/account/password"

func credentialJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func changeBody(t *testing.T, old, next string) string {
	return credentialJSON(t, map[string]string{"currentPassword": old, "newPassword": next})
}

func registerPassword(t *testing.T, h http.Handler, origin, name, password string) *httptest.ResponseRecorder {
	t.Helper()
	anon := sessionCookie(t, call(h, "GET", "/api/v1/session", "", "", "", ""))
	input := auth.Registration{Email: name + "@example.test", Username: name, FirstName: "Test", LastName: "User", Password: password}
	return call(h, "POST", "/api/v1/register", credentialJSON(t, input), anon, auth.CSRF(anon), origin)
}

func assertNoCredentialMutation(t *testing.T, a *API, user store.User, tokens ...string) {
	t.Helper()
	var current store.User
	if err := a.Store.DB.First(&current, user.ID).Error; err != nil || current.Password != user.Password {
		t.Fatal("rejected request changed the password")
	}
	for _, token := range tokens {
		if _, err := a.Auth.Load(context.Background(), token); err != nil {
			t.Fatal("rejected request ended a session")
		}
	}
	var queued int
	if err := a.Store.SQL.QueryRow("SELECT count(*) FROM email_outbox").Scan(&queued); err != nil || queued != 0 {
		t.Fatal("rejected request queued email")
	}
}

func TestPasswordEndpointRotatesCookieAndRejectsInvalidRequests(t *testing.T) {
	a, h := testAPI(t)
	ctx := context.Background()
	const old = "credential canary � original"
	user, err := a.Auth.Register(ctx, auth.Registration{Email: "password@example.test", Username: "password", FirstName: "Pass", LastName: "Word", Password: old})
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.Auth.Rotate(ctx, &user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Auth.Rotate(ctx, &user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	anon, err := a.Auth.Rotate(ctx, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	newPassword := strings.Repeat("🌻", 128)
	valid := changeBody(t, old, newPassword)
	for _, tc := range []struct {
		name, token, csrf, body, code string
		want                          int
	}{
		{"no session", "", "", valid, "unauthenticated", 401},
		{"anonymous", anon, auth.CSRF(anon), valid, "unauthenticated", 401},
		{"csrf", first, "stale", valid, "csrf_invalid", 403},
		{"missing current", first, auth.CSRF(first), changeBody(t, "", newPassword), "current_password_incorrect", 400},
		{"wrong current", first, auth.CSRF(first), changeBody(t, "wrong", "short"), "current_password_incorrect", 400},
		{"minimum", first, auth.CSRF(first), changeBody(t, old, "short"), "password_length", 400},
		{"blocklist", first, auth.CSRF(first), changeBody(t, old, "1234567890"), "password_common", 400},
		{"reuse", first, auth.CSRF(first), changeBody(t, old, old), "password_reused", 400},
		{"raw bytes", first, auth.CSRF(first), strings.Replace(valid, "�", "\xff", 1), "invalid_input", 400},
		{"lone surrogate", first, auth.CSRF(first), strings.Replace(valid, "�", `\ud800`, 1), "invalid_input", 400},
		{"oversized credential", first, auth.CSRF(first), changeBody(t, old, newPassword+"x"), "invalid_input", 400},
		{"confirmation is not an API field", first, auth.CSRF(first), strings.TrimSuffix(valid, "}") + `,"confirmation":"value"}`, "invalid_input", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := call(h, "PUT", passwordPath, tc.body, tc.token, tc.csrf, a.Config.Origin)
			if w.Code != tc.want || !strings.Contains(w.Body.String(), tc.code) || w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("unexpected response status or error code: %d", w.Code)
			}
			for _, secret := range []string{old, newPassword, user.Password, first, second} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatal("a rejected request exposed a credential canary")
				}
			}
			assertNoCredentialMutation(t, a, user, first, second)
		})
	}
	w := call(h, "PUT", passwordPath, valid, first, auth.CSRF(first), a.Config.Origin)
	if w.Code != 200 || w.Header().Get("X-MyChef-User") != user.Profile().ID {
		t.Fatalf("password change returned %d", w.Code)
	}
	rotated := sessionCookie(t, w)
	if rotated == first || !strings.Contains(w.Body.String(), auth.CSRF(rotated)) {
		t.Fatal("the password-change response did not rotate both cookie and CSRF")
	}
	if _, err := a.Auth.Load(ctx, second); !errors.Is(err, auth.ErrSession) {
		t.Fatal("other browser kept its session")
	}
	w = call(h, "POST", "/api/v1/login", credentialJSON(t, map[string]string{"email": user.Email, "password": newPassword}), anon, auth.CSRF(anon), a.Config.Origin)
	if w.Code != 200 || sessionCookie(t, w) == anon {
		t.Fatal("a fresh sign-in did not accept the changed 512-byte credential")
	}
}

func TestCredentialRoutesRejectMalformedUnicodeWithoutSubstituting(t *testing.T) {
	a, h := testAPI(t)
	origin := a.Config.Origin
	const password = "literal � test phrase"
	anon := sessionCookie(t, call(h, "GET", "/api/v1/session", "", "", "", ""))
	registration := credentialJSON(t, auth.Registration{Email: "utf@example.test", Username: "utf", FirstName: "UTF", LastName: "Test", Password: password})
	bad := func(good, replacement string) string { return strings.Replace(good, "�", replacement, 1) }
	for _, malformed := range []string{"\xff", `\ud800`} {
		w := call(h, "POST", "/api/v1/register", bad(registration, malformed), anon, auth.CSRF(anon), origin)
		if w.Code != 400 || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("malformed registration returned %d", w.Code)
		}
	}
	var users int
	if err := a.Store.SQL.QueryRow("SELECT count(*) FROM users").Scan(&users); err != nil || users != 0 {
		t.Fatal("malformed registration created an account")
	}
	w := call(h, "POST", "/api/v1/register", registration, anon, auth.CSRF(anon), origin)
	if w.Code != 201 {
		t.Fatal("explicit U+FFFD did not register")
	}
	anon = sessionCookie(t, call(h, "GET", "/api/v1/session", "", "", "", ""))
	login := credentialJSON(t, map[string]string{"email": "utf@example.test", "password": password})
	for _, malformed := range []string{"\xff", `\ud800`} {
		w := call(h, "POST", "/api/v1/login", bad(login, malformed), anon, auth.CSRF(anon), origin)
		if w.Code != 400 || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("malformed login returned %d", w.Code)
		}
	}
	w = call(h, "POST", "/api/v1/login", login, anon, auth.CSRF(anon), origin)
	if w.Code != 200 {
		t.Fatal("explicit U+FFFD did not sign in")
	}
}

func TestPasswordPolicyNormalizedBoundariesAndContract(t *testing.T) {
	a, h := testAPI(t)
	normalizedShort := strings.Repeat("क़", 5)
	normalizedLong := strings.Repeat("e\u0301", 128)
	for i, pair := range [][2]string{{normalizedShort, normalizedLong}, {normalizedLong, normalizedShort}} {
		name := []string{"normalized_a", "normalized_b"}[i]
		w := registerPassword(t, h, a.Config.Origin, name, pair[0])
		if w.Code != 201 {
			t.Fatalf("registration rejected a valid normalized boundary: %d", w.Code)
		}
		token := sessionCookie(t, w)
		w = call(h, "PUT", passwordPath, changeBody(t, pair[0], pair[1]), token, auth.CSRF(token), a.Config.Origin)
		if w.Code != 200 {
			t.Fatalf("change rejected a valid normalized boundary: %d", w.Code)
		}
	}
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]map[string]any
			}
		}
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	for _, field := range [][2]string{{"Login", "password"}, {"SignUpCompletion", "password"}, {"EmailChangeStart", "currentPassword"}, {"PasswordChange", "currentPassword"}, {"PasswordChange", "newPassword"}} {
		p := contract.Components.Schemas[field[0]].Properties[field[1]]
		desc, _ := p["description"].(string)
		if p["type"] != "string" || p["minLength"] != nil || p["maxLength"] != nil || !strings.Contains(desc, "512") {
			t.Fatal("OpenAPI credential fields must describe byte bounds without raw character limits")
		}
	}
}

func TestPasswordEndpointThrottlesWithoutMutation(t *testing.T) {
	a, h := testAPI(t)
	ctx := context.Background()
	const old = "password before throttle"
	u, err := a.Auth.Register(ctx, auth.Registration{Email: "throttle@example.test", Username: "throttle", FirstName: "Test", LastName: "User", Password: old})
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.Auth.Rotate(ctx, &u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		w := call(h, "PUT", passwordPath, changeBody(t, "wrong", "a replacement test phrase"), token, auth.CSRF(token), a.Config.Origin)
		if w.Code != 400 {
			t.Fatalf("an allowed guess returned %d", w.Code)
		}
	}
	w := call(h, "PUT", passwordPath, changeBody(t, old, "a replacement test phrase"), token, auth.CSRF(token), a.Config.Origin)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("blocked attempt returned %d or omitted retry details", w.Code)
	}
	assertNoCredentialMutation(t, a, u, token)
}

func TestPasswordChangeDiagnosticsOmitCredentials(t *testing.T) {
	a, _ := testAPI(t)
	var logs bytes.Buffer
	a.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	h := a.Handler(nil)
	ctx := context.Background()
	const old = "old diagnostic password canary"
	const next = "new diagnostic password canary"
	const recipient = "password-diagnostic-recipient@example.test"
	u, err := a.Auth.Register(ctx, auth.Registration{Email: recipient, Username: "diagnostic", FirstName: "Safe", LastName: "Test", Password: old})
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.Auth.Rotate(ctx, &u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.SQL.Exec("CREATE TRIGGER diagnostic_outbox_failure BEFORE INSERT ON email_outbox BEGIN SELECT RAISE(ABORT, 'injected notice failure'); END"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body string
		code int
	}{
		{changeBody(t, "wrong diagnostic password", next), 400},
		{changeBody(t, old, next), 503},
		{strings.TrimSuffix(changeBody(t, old, next), "}") + `,"recipient":"` + recipient + `"}`, 400},
	} {
		w := call(h, "PUT", passwordPath, tc.body, token, auth.CSRF(token), a.Config.Origin)
		if w.Code != tc.code {
			t.Fatalf("diagnostic path returned %d", w.Code)
		}
		for _, canary := range []string{old, next, recipient, u.Password, token, auth.Hash(token),
			"wrong diagnostic password", "Your MyChef password changed", "injected notice failure"} {
			if strings.Contains(w.Body.String(), canary) || strings.Contains(logs.String(), canary) {
				t.Fatal("a credential or notice canary entered a response or log")
			}
		}
	}
	if logs.Len() == 0 {
		t.Fatal("storage failure did not exercise the logger")
	}
	assertNoCredentialMutation(t, a, u, token)
}
