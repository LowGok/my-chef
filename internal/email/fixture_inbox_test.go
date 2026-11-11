package email

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/config"
)

func TestFixtureInboxConsumesMessagesAndKeepsProofsOutOfErrors(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	origin := "https://preview.example.test"
	capability := bytes.Repeat([]byte{7}, 32)
	token := base64.RawURLEncoding.EncodeToString(capability)
	inbox := NewFixtureInbox(config.FixtureInbox{Origin: origin, CapabilityHash: sha256.Sum256(capability), Expires: now.Add(30 * time.Minute)},
		func() time.Time { return now })
	sender := fakeSender{inbox: inbox}
	const proof = "https://preview.example.test/app/signup#synthetic-fixture-proof"
	send := func(key, address string) {
		t.Helper()
		body, _ := json.Marshal(sendPayload{To: []string{address}, Text: proof})
		if got := sender.send(context.Background(), key, body); got.id == "" {
			t.Fatal("fake delivery failed")
		}
	}
	poll := func(site, auth, addr string) *httptest.ResponseRecorder {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"recipient": addr})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/fixture/inbox", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", site)
		req.Header.Set("Authorization", "Bearer "+auth)
		w := httptest.NewRecorder()
		inbox.ServeHTTP(w, req)
		return w
	}
	const recipient = "fixture@example.test"
	send("proof/1", recipient)
	for _, check := range [][2]string{{origin, "wrong"}, {"https://elsewhere.example.test", token}} {
		if got := poll(check[0], check[1], recipient); got.Code != http.StatusNotFound || strings.Contains(got.Body.String(), proof) {
			t.Fatal("unauthorized request read a fixture proof")
		}
	}
	if got := poll(origin, token, recipient); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), proof) ||
		got.Header().Get("Cache-Control") != "no-store" || got.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("authorized fixture read did not return a non-cacheable proof")
	}
	send("proof/1", recipient)
	if got := poll(origin, token, recipient); got.Code != http.StatusNoContent {
		t.Fatal("a duplicate send recreated a consumed message")
	}
	send("proof/page", recipient)
	form := url.Values{"capability": {token}, "recipient": {recipient}}
	req := httptest.NewRequest(http.MethodPost, "/fixture/inbox", strings.NewReader(form.Encode()))
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	page := httptest.NewRecorder()
	inbox.ServeHTTP(page, req)
	if page.Code != http.StatusOK || page.Header().Get("Cache-Control") != "no-store" ||
		page.Header().Get("Referrer-Policy") != "same-origin" ||
		!strings.Contains(page.Body.String(), proof) || strings.Contains(page.Body.String(), token) {
		t.Fatal("manual fixture read exposed its capability or omitted its proof")
	}
	send("proof/2", "recipient@example.com")
	if got := poll(origin, token, "recipient@example.com"); got.Code == http.StatusOK {
		t.Fatal("fixture inbox captured a non-synthetic address")
	}
	send("proof/3", recipient)
	now = now.Add(10 * time.Minute)
	if got := poll(origin, token, recipient); got.Code != http.StatusNoContent {
		t.Fatal("an expired message could still be read")
	}
	send("proof/4", recipient)
	now = now.Add(20 * time.Minute)
	if got := poll(origin, token, recipient); got.Code != http.StatusNotFound {
		t.Fatal("an expired fixture capability still worked")
	}
}
