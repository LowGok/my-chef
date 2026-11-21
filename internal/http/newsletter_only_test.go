package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
)

func TestNewsletterOnlyMuxRestrictsSurface(t *testing.T) {
	a, _ := newsletterAPI(t)
	a.Config.Version = "role-fixture"
	a.Config.NewsletterChecks = func(context.Context) map[string]bool {
		return map[string]bool{"collectionEnabled": false, "storage": true}
	}
	// Even accidentally populated full-app hooks must remain unreachable.
	a.Config.EmailWebhook = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(202) })
	a.Config.FixtureInbox = a.Config.EmailWebhook
	h := a.NewsletterHandler()
	for _, path := range []string{"/", "/app/", "/app/signup", "/api/v1/login", "/api/v1/register", "/api/v1/signup/complete", "/api/v1/profile", "/api/v1/menus", "/api/v1/account/export", "/api/v1/analytics/consent", "/api/native/v1/newsletter/subscribe", "/api/native/v1/login", "/api/v1/webhooks/email", "/fixture/inbox", "/api/v1/fixture/inbox"} {
		for _, method := range []string{"GET", "POST", "OPTIONS"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			if w.Code != 404 {
				t.Fatalf("%s %s exposed: %d", method, path, w.Code)
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Robots-Tag") != "noindex" {
				t.Fatal("role response can be cached/indexed")
			}
		}
	}
	for _, path := range []string{"/api/v1/newsletter/subscribe", "/api/v1/newsletter/confirm", "/api/v1/newsletter/unsubscribe"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 405 {
			t.Fatalf("GET action %s: %d", path, w.Code)
		}
	}
	for _, token := range []string{"", strings.Repeat("s", 64)} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/internal/newsletter/readiness", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		h.ServeHTTP(w, r)
		if token == "" {
			if w.Code != 401 {
				t.Fatal("unprotected readiness")
			}
			continue
		}
		var body struct {
			Role, Mode, Version string
			Ready               bool
			Checks              map[string]bool
		}
		if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Role != "newsletter" || body.Mode != "fixture" || body.Version != "role-fixture" || body.Ready || !body.Checks["storage"] {
			t.Fatalf("unbound readiness: %s", w.Body)
		}
	}
}

func TestNewsletterOnlyPausePreservesValidProofActions(t *testing.T) {
	a, _ := newsletterAPI(t)
	h := a.NewsletterHandler()
	token := strings.Repeat("s", 64)
	w := newsletterCall(h, "/api/v1/newsletter/subscribe", `{"email":"role@example.test","locale":"en","consentVersion":"newsletter-v1"}`, token, "https://mychef.dev")
	if w.Code != 202 {
		t.Fatalf("capture: %d %s", w.Code, w.Body)
	}
	if _, err := a.Config.Newsletter.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	var row mailqueue.Record
	if err := a.Store.DB.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	fields, _, err := mailqueue.OpenProtected(a.Config.Newsletter.Keys, row, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	a.Config.NewsletterReady = func(context.Context) bool { return false }
	w = newsletterCall(h, "/api/v1/newsletter/subscribe", `{}`, token, "https://mychef.dev")
	if w.Code != 503 {
		t.Fatal("paused capture accepted")
	}
	for _, action := range []string{"confirm", "unsubscribe"} {
		_, proof, ok := strings.Cut(fields[action], "#")
		if !ok {
			t.Fatal("no proof")
		}
		raw, _ := json.Marshal(map[string]string{"proof": proof})
		w = newsletterCall(h, "/api/v1/newsletter/"+action, string(raw), token, "https://mychef.dev")
		if w.Code != 200 {
			t.Fatalf("paused %s: %d %s", action, w.Code, w.Body)
		}
	}
}
