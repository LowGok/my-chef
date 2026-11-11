package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
)

func TestExistingEmailVerificationRestrictsAndRotatesSessions(t *testing.T) {
	a, _ := testAPI(t)
	ctx := context.Background()
	keys, err := mailqueue.NewProofKeys("test", map[string][]byte{"test": bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	a.Config.EmailRateKey = bytes.Repeat([]byte{2}, 32)
	a.Config.Verification = &auth.EmailVerification{SignUp: &auth.SignUp{Store: a.Store, Keys: keys,
		RateKey: a.Config.EmailRateKey, Origin: a.Config.Origin}}
	h := a.Handler(nil)
	const display = "MixedCase@Example.Test"
	const password = "legacy account password fixture"
	user, err := a.Auth.Register(ctx, auth.Registration{Email: display, Password: password, Username: "legacy",
		FirstName: "Private", LastName: "User"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.Auth.Register(ctx, auth.Registration{Email: "other@example.test", Password: password, Username: "other",
		FirstName: "Other", LastName: "User"})
	if err != nil {
		t.Fatal(err)
	}
	login := func() string {
		t.Helper()
		anonymous := sessionCookie(t, call(h, "GET", "/api/v1/session", "", "", "", ""))
		w := call(h, "POST", "/api/v1/login", `{"email":"mixedcase@example.test","password":"legacy account password fixture"}`,
			anonymous, auth.CSRF(anonymous), a.Config.Origin)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"verificationRequired":true`) ||
			!strings.Contains(w.Body.String(), `"user":null`) || strings.Contains(w.Body.String(), "Private") {
			t.Fatal("restricted sign-in returned private profile data")
		}
		return sessionCookie(t, w)
	}
	first, second, third := login(), login(), login()
	w := call(h, "GET", "/api/v1/session", "", first, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"verificationRequired":true`) ||
		!strings.Contains(w.Body.String(), `"user":null`) {
		t.Fatal("restricted session lookup exposed private data")
	}
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/profile"}, {"PUT", "/api/v1/profile"}, {"PUT", "/api/v1/account/password"},
		{"POST", "/api/v1/generations"}, {"GET", "/api/v1/generations/1"}, {"GET", "/api/v1/menus"},
		{"GET", "/api/v1/menus/1"}, {"GET", "/api/v1/recipes/1"}, {"GET", "/api/v1/favorites"},
		{"PUT", "/api/v1/recipes/1/favorite"}, {"DELETE", "/api/v1/recipes/1/favorite"},
		{"GET", "/api/v1/favorite-weeks/2026-11-02"}, {"POST", "/api/v1/favorites/1/queue"},
		{"POST", "/api/v1/favorites/1/swap"}, {"GET", "/api/v1/shopping/2026-11-02"}, {"PUT", "/api/v1/shopping/2026-11-02"},
	} {
		w := call(h, route.method, route.path, "{}", first, auth.CSRF(first), a.Config.Origin)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "email_verification_required") ||
			w.Header().Get("X-MyChef-User") != "" {
			t.Fatalf("restricted session reached %s %s: %d", route.method, route.path, w.Code)
		}
	}
	post := func(path, body, token string) int {
		t.Helper()
		w := call(h, "POST", path, body, token, auth.CSRF(token), a.Config.Origin)
		if len(w.Result().Cookies()) != 0 {
			t.Fatal("a rejected proof or resend altered the session cookie")
		}
		return w.Code
	}
	if post("/api/v1/email/verification/resend", "{}", first) != http.StatusAccepted {
		t.Fatal("restricted session could not request verification")
	}
	var mail mailqueue.Record
	if err := a.Store.DB.Where("recipient = ?", display).First(&mail).Error; err != nil {
		t.Fatal("verification did not use the stored delivery spelling")
	}
	fields, _, err := mailqueue.OpenProtected(keys, mail, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(fields["link"])
	if err != nil || link.Path != "/app/verify-email" || link.Fragment == "" {
		t.Fatal("missing verification proof in protected message")
	}
	body, _ := json.Marshal(map[string]string{"proof": link.Fragment})
	otherToken, err := a.Auth.Rotate(ctx, &other.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	anonymous := sessionCookie(t, call(h, "GET", "/api/v1/session", "", "", "", ""))
	if post("/api/v1/email/verification/confirm", string(body), otherToken) != 400 ||
		post("/api/v1/email/verification/confirm", string(body), anonymous) != 401 {
		t.Fatal("a different browser could consume the verification proof")
	}
	if code := call(h, "POST", "/api/v1/logout", "", first, auth.CSRF(first), a.Config.Origin).Code; code != 204 {
		t.Fatalf("restricted sign-out failed: %d", code)
	}
	if _, _, err := a.Config.Verification.Confirm(ctx, user.ID, first, link.Fragment); !errors.Is(err, auth.ErrSession) {
		t.Fatal("a verification that lost the sign-out race changed the account")
	}
	w = call(h, "POST", "/api/v1/email/verification/confirm", string(body), second, auth.CSRF(second), a.Config.Origin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Private") {
		t.Fatalf("matching session could not verify: %d", w.Code)
	}
	replacement := sessionCookie(t, w)
	if replacement == second || call(h, "GET", "/api/v1/profile", "", second, "", "").Code != 401 ||
		call(h, "GET", "/api/v1/profile", "", third, "", "").Code != 401 ||
		call(h, "GET", "/api/v1/profile", "", replacement, "", "").Code != 200 {
		t.Fatal("verification did not replace the restricted session")
	}
	if _, err := a.Auth.Load(ctx, otherToken); err != nil {
		t.Fatal("verification revoked another account's session")
	}
	if post("/api/v1/email/verification/confirm", string(body), replacement) != 400 {
		t.Fatal("verified link could be used again")
	}
}
