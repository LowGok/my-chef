package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/newsletter"
)

func newsletterAPI(t *testing.T) (*API, http.Handler) {
	t.Helper()
	a, _ := testAPI(t)
	keys, err := mailqueue.NewProofKeys("test", map[string][]byte{"test": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	a.Config.Newsletter = &newsletter.Service{Store: a.Store, Keys: keys, RateKey: bytes.Repeat([]byte{8}, 32), Origin: "https://mychef.dev", ConsentVersion: "newsletter-v1"}
	a.Config.NewsletterSettings = config.Newsletter{Mode: "fixture", ServiceToken: strings.Repeat("s", 64), Origin: "https://mychef.dev", ConsentVersion: "newsletter-v1"}
	a.Config.NewsletterReady = func(context.Context) bool { return true }
	return a, a.Handler(nil)
}

func newsletterCall(h http.Handler, path, body, token, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	r.Header.Set("X-MyChef-Newsletter-Source", "192.0.2.23")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestNewsletterHTTPRequiresServiceAndDurableAcknowledgement(t *testing.T) {
	a, h := newsletterAPI(t)
	body := `{"email":"reader@example.test","locale":"en","consentVersion":"newsletter-v1"}`
	for _, attempt := range []struct {
		token, origin string
		want          int
	}{{"bad", "https://mychef.dev", 401}, {strings.Repeat("s", 64), "https://other.test", 403}, {strings.Repeat("s", 64), "https://mychef.dev", 202}} {
		w := newsletterCall(h, "/api/v1/newsletter/subscribe", body, attempt.token, attempt.origin)
		if w.Code != attempt.want {
			t.Fatalf("status %d expected %d: %s", w.Code, attempt.want, w.Body.String())
		}
		for _, secret := range []string{"reader@example.test", strings.Repeat("s", 64), "userID", "isNewsletterOnly"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("private data in response")
			}
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("capture response is cacheable")
		}
	}
	var jobs int
	if err := a.Store.SQL.QueryRow("SELECT count(*) FROM newsletter_requests").Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal("acknowledgement was not durable", err)
	}
	if worked, err := a.Config.Newsletter.Step(t.Context()); err != nil || !worked {
		t.Fatal("acknowledged request did not process", err)
	}
	w := newsletterCall(h, "/api/v1/newsletter/subscribe", body, strings.Repeat("s", 64), "https://mychef.dev")
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("address throttle not applied")
	}
}

func TestNewsletterHTTPRejectsInvalidAndUnavailableRequests(t *testing.T) {
	a, h := newsletterAPI(t)
	for _, body := range []string{`{"email":"real@example.com","locale":"en","consentVersion":"newsletter-v1"}`, `{"email":"a@example.test","locale":"en","consentVersion":"newsletter-v1","userID":"1"}`, strings.Repeat("a", 4097)} {
		w := newsletterCall(h, "/api/v1/newsletter/subscribe", body, strings.Repeat("s", 64), "https://mychef.dev")
		if w.Code != 400 && w.Code != 413 {
			t.Fatalf("invalid request returned %d", w.Code)
		}
	}
	a.Config.NewsletterReady = func(context.Context) bool { return false }
	h = a.Handler(nil)
	w := newsletterCall(h, "/api/v1/newsletter/subscribe", `{}`, strings.Repeat("s", 64), "https://mychef.dev")
	if w.Code != 503 {
		t.Fatalf("unready backend accepted capture: %d", w.Code)
	}
	w = newsletterCall(h, "/api/v1/newsletter/confirm", `{"proof":"bad"}`, strings.Repeat("s", 64), "https://mychef.dev")
	if w.Code != 400 {
		t.Fatalf("invalid proof status %d", w.Code)
	}
}
