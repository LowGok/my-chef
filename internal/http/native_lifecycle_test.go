package httpapi

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
)

func TestNativeUnverifiedAccountExportsAndDeletesWithoutBrowserCookies(t *testing.T) {
	a, _, user, cookie := lifecycleHTTPFixture(t)
	a.Config.Verification = &auth.EmailVerification{}
	h := a.Handler(nil)
	tokens, err := a.Auth.NativeBind(t.Context(), cookie, "ios", "")
	if err != nil {
		t.Fatal(err)
	}
	if callNative(h, "GET", "/profile", "", tokens.AccessToken).Code != 403 {
		t.Fatal("native unverified account bypassed verification")
	}
	w := callNative(h, "POST", "/account/export", `{"currentPassword":"`+lifecyclePassword+`"}`, tokens.AccessToken)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("native unverified account could not export its own data")
	}
	r := httptest.NewRequest("POST", "/api/native/v1/account/deletion", strings.NewReader(`{"currentPassword":"`+lifecyclePassword+`","confirmation":"DELETE"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	r.Header.Set("Idempotency-Key", strings.Repeat("5", 32))
	w = httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(w, r); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("native deletion held the shared account-work lock")
	}
	if w.Code != 200 || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("native deletion failed or wrote a browser cookie")
	}
	if _, err := a.Auth.NativeRefresh(t.Context(), tokens.RefreshToken); !errors.Is(err, auth.ErrSession) {
		t.Fatal("deleted native account retained refresh authority")
	}
	var remaining int64
	if err := a.Store.DB.Table("users").Where("user_id=?", user.ID).Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatal("native deletion retained its account")
	}
}

func TestNativePasswordHTTPReplacesTheFamilyWithoutCookies(t *testing.T) {
	a, h, _, cookie := lifecycleHTTPFixture(t)
	tokens, err := a.Auth.NativeBind(t.Context(), cookie, "android", "")
	if err != nil {
		t.Fatal(err)
	}
	next := nativeReply(t, callNative(h, "PUT", "/account/password", `{"currentPassword":"`+lifecyclePassword+`","newPassword":"Garden replacement native 2026!"}`, tokens.AccessToken), 200)
	if _, err := a.Auth.NativeRefresh(t.Context(), tokens.RefreshToken); !errors.Is(err, auth.ErrSession) {
		t.Fatal("old native family survived password change")
	}
	if callNative(h, "GET", "/session", "", next.AccessToken).Code != 200 {
		t.Fatal("current installation did not receive its replacement")
	}
}
