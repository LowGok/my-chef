package httpapi

import (
	"net/http"
	"testing"

	"github.com/LowGok/my-chef/internal/auth"
)

func TestAnalyticsRoutesDefaultDisabledAndStrict(t *testing.T) {
	a, h := testAPI(t)
	notice := call(h, "GET", "/api/v1/analytics/notice", "", "", "", "")
	if notice.Code != 200 {
		t.Fatalf("notice %d", notice.Code)
	}
	u, e := a.Auth.Register(t.Context(), auth.Registration{Email: "analytics@example.invalid", Password: "ChefTest2026!", Username: "analytics", FirstName: "Test", LastName: "Fixture"})
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Store.DB.Model(&u).Update("email_verified_at", 1).Error; e != nil {
		t.Fatal(e)
	}
	token, e := a.Auth.Rotate(t.Context(), &u.ID, "")
	if e != nil {
		t.Fatal(e)
	}
	for _, route := range []string{"/api/v1/analytics/preferences"} {
		r := call(h, http.MethodGet, route, "", token, "", "")
		if r.Code != 200 {
			t.Fatalf("preferences %d %s", r.Code, r.Body.String())
		}
		if r.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("cacheable preference")
		}
		if code := call(h, "PUT", route, `{"enabled":true,"noticeVersion":"core-v1","expectedRevision":0}`, token, auth.CSRF(token), a.Config.Origin).Code; code != 503 {
			t.Fatalf("disabled enable=%d", code)
		}
		if code := call(h, "PUT", route, `{"enabled":false,"noticeVersion":"core-v1","expectedRevision":0,"events":[]}`, token, auth.CSRF(token), a.Config.Origin).Code; code != 400 {
			t.Fatalf("extra fields=%d", code)
		}
		if code := call(h, "PUT", route, `{"noticeVersion":"core-v1","expectedRevision":0}`, token, auth.CSRF(token), a.Config.Origin).Code; code != 400 {
			t.Fatalf("missing boolean=%d", code)
		}
	}
}

func TestNativeAnalyticsPreferenceUsesBearerGuards(t *testing.T) {
	for _, platform := range []string{"ios", "android"} {
		t.Run(platform, func(t *testing.T) {
			a, h := testAPI(t)
			_, e := a.Auth.Register(t.Context(), auth.Registration{Email: "native-analytics@example.invalid", Password: "ChefTest2026!", Username: "nativeanalytics", FirstName: "Native", LastName: "Fixture"})
			if e != nil {
				t.Fatal(e)
			}
			if e = a.Store.DB.Exec("UPDATE users SET email_verified_at=1").Error; e != nil {
				t.Fatal(e)
			}
			anonymous := nativeReply(t, callNative(h, "POST", "/session", `{"platform":"`+platform+`"}`, ""), 201)
			tokens := nativeReply(t, callNative(h, "POST", "/login", `{"email":"native-analytics@example.invalid","password":"ChefTest2026!"}`, anonymous.AccessToken), 200)
			if w := callNative(h, "GET", "/analytics/preferences", "", tokens.AccessToken); w.Code != 200 || w.Header().Get("Set-Cookie") != "" {
				t.Fatal("native preferences", w.Code)
			}
			if w := callNative(h, "PUT", "/analytics/preferences", `{"enabled":false,"noticeVersion":"","expectedRevision":0}`, tokens.AccessToken); w.Code != 200 {
				t.Fatal("native off required browser CSRF", w.Code)
			}
			if w := callNative(h, "GET", "/analytics/preferences", "", ""); w.Code != 401 {
				t.Fatal("anonymous read", w.Code)
			}
		})
	}
}
