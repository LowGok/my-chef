package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

const personalFixturePath = "/api/v1/fixture/personal-recipes/entitlement"

func TestPersonalFixturePermissionsRequireCapabilityAndSyntheticIncarnation(t *testing.T) {
	a, _ := testAPI(t)
	now := time.Now().UTC().Truncate(time.Second).Add(time.Millisecond)
	verified := now.Unix()
	user := store.User{ID: 1, FirstName: "Fixture", LastName: "Chef", Email: "personal@example.test", EmailKey: "personal@example.test", Username: "personal", Password: "fixture-hash", EmailVerifiedAt: &verified}
	if err := a.Store.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	var incarnation string
	if err := a.Store.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=1").Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	raw := bytes.Repeat([]byte{17}, 32)
	capability := base64.RawURLEncoding.EncodeToString(raw)
	settings := config.FixtureInbox{Origin: a.Config.Origin, CapabilityHash: sha256.Sum256(raw), Expires: now.Add(20 * time.Minute)}
	fixture := NewPersonalFixture(a.Store, settings, func() time.Time { return now })
	a.Config.PersonalFixture = fixture
	a.Config.PersonalEntitlements = fixture.ReadEntitlement
	handler := a.Handler(nil)
	request := func(method, origin, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, personalFixturePath, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), capability) || strings.Contains(w.Body.String(), user.Email) {
			t.Fatal("fixture diagnostic exposed private values")
		}
		return w
	}
	grant := func(origin, token, body string) *httptest.ResponseRecorder {
		return request(http.MethodPost, origin, token, body)
	}
	readGeneration := func(origin, token string) string {
		t.Helper()
		w := request(http.MethodGet, origin, token, "")
		var info struct{ Generation string }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &info) != nil || len(info.Generation) != 32 {
			t.Fatalf("fixture generation unavailable: %d", w.Code)
		}
		return info.Generation
	}
	for _, bad := range []struct{ origin, token string }{{"https://foreign.example.test", capability}, {a.Config.Origin, "wrong"}} {
		if w := request(http.MethodGet, bad.origin, bad.token, ""); w.Code != 404 {
			t.Fatal("fixture generation disclosed")
		}
	}
	generation := readGeneration(a.Config.Origin, capability)
	nextOperation := 0
	body := func(instance, state string) string {
		nextOperation++
		data, err := json.Marshal(map[string]string{"accountId": "1", "accountInstance": instance, "state": state, "generation": generation, "operationId": fmt.Sprintf("%032x", nextOperation)})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	read := func(instance string) store.PersonalEntitlement {
		t.Helper()
		var out store.PersonalEntitlement
		err := a.Store.DB.Transaction(func(tx *gorm.DB) error {
			var err error
			out, err = a.Config.PersonalEntitlements(context.Background(), tx, 1, instance)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if read(incarnation).Premium {
		t.Fatal("default fixture granted Premium")
	}
	for _, attempt := range []struct{ origin, token, body string }{
		{"https://foreign.example.test", capability, body(incarnation, "premium")},
		{a.Config.Origin, "wrong", body(incarnation, "premium")},
		{a.Config.Origin, capability, body(strings.Repeat("0", 32), "premium")},
		{a.Config.Origin, capability, body(incarnation, "unlimited")},
		{a.Config.Origin, capability, `{"accountId":"1","accountInstance":"` + incarnation + `","state":"free","state":"premium"}`},
		{a.Config.Origin, capability, `{"accountId":"1","accountInstance":"` + incarnation + `","state":"premium","extra":true}`},
	} {
		if got := grant(attempt.origin, attempt.token, attempt.body); got.Code != http.StatusNotFound {
			t.Fatal("unsafe fixture grant accepted", got.Code)
		}
	}
	if read(incarnation).Premium {
		t.Fatal("failed attempts changed entitlement")
	}
	for _, state := range []string{"premium", "expired", "free", "premium"} {
		got := grant(a.Config.Origin, capability, body(incarnation, state))
		if got.Code != http.StatusNoContent || got.Header().Get("Cache-Control") != "no-store" || got.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("valid fixture grant failed or cached", got.Code)
		}
		value := read(incarnation)
		if state == "premium" && (!value.Premium || value.ExpiresUnix != settings.Expires.Unix()+1) || state == "expired" && value.Premium || state == "free" && value.Premium {
			t.Fatal("fixture state mismatch", state)
		}
	}
	original := body(incarnation, "premium")
	if w := grant(a.Config.Origin, capability, original); w.Code != 204 {
		t.Fatal("grant not created", w.Code)
	}
	restarted, err := store.Open(a.Store.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	sameFixture := NewPersonalFixture(restarted, settings, func() time.Time { return now })
	a.Config.PersonalFixture = sameFixture
	a.Config.PersonalEntitlements = sameFixture.ReadEntitlement
	handler = a.Handler(nil)
	if readGeneration(a.Config.Origin, capability) != generation || !read(incarnation).Premium {
		t.Fatal("same lease lost grant at restart")
	}
	if w := grant(a.Config.Origin, capability, body(incarnation, "free")); w.Code != 204 {
		t.Fatal("revoke failed", w.Code)
	}
	if w := grant(a.Config.Origin, capability, original); w.Code != 204 || read(incarnation).Premium {
		t.Fatal("grant retry reactivated revoked access", w.Code)
	}
	conflict := strings.Replace(original, `"premium"`, `"free"`, 1)
	if w := grant(a.Config.Origin, capability, conflict); w.Code != 404 {
		t.Fatal("conflicting retry accepted", w.Code)
	}
	concurrent := body(incarnation, "premium")
	var requests sync.WaitGroup
	for range 8 {
		requests.Go(func() {
			if w := grant(a.Config.Origin, capability, concurrent); w.Code != 204 {
				t.Errorf("identical concurrent operation failed: %d", w.Code)
			}
		})
	}
	requests.Wait()
	if !read(incarnation).Premium {
		t.Fatal("concurrent grant unavailable")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if value, _ := fixture.ReadEntitlement(cancelled, a.Store.DB, 1, incarnation); value.Premium {
		t.Fatal("cancelled permission read still granted access")
	}
	if err := a.Store.DB.Model(&store.User{}).Where("user_id=1").Update("email", "real@example.com").Error; err != nil {
		t.Fatal(err)
	}
	if read(incarnation).Premium || grant(a.Config.Origin, capability, body(incarnation, "premium")).Code != http.StatusNotFound {
		t.Fatal("real-email account gained fixture permission")
	}
	if err := a.Store.DB.Model(&store.User{}).Where("user_id=1").Updates(map[string]any{"email": user.Email, "email_verified_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	if read(incarnation).Premium || grant(a.Config.Origin, capability, body(incarnation, "premium")).Code != http.StatusNotFound {
		t.Fatal("unverified account gained fixture permission")
	}
	if err := a.Store.DB.Delete(&store.User{}, 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.Store.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	var replacement string
	if err := a.Store.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=1").Scan(&replacement); err != nil {
		t.Fatal(err)
	}
	if replacement == incarnation || read(replacement).Premium || grant(a.Config.Origin, capability, body(incarnation, "premium")).Code != http.StatusNotFound {
		t.Fatal("reused account ID inherited a grant")
	}
	if grant(a.Config.Origin, capability, body(replacement, "premium")).Code != http.StatusNoContent {
		t.Fatal("new fixture incarnation could not be granted deliberately")
	}
	oldRequest := body(replacement, "premium")
	newRaw := bytes.Repeat([]byte{19}, 32)
	newCapability := base64.RawURLEncoding.EncodeToString(newRaw)
	newSettings := settings
	newSettings.CapabilityHash = sha256.Sum256(newRaw)
	rotatedFixture := NewPersonalFixture(a.Store, newSettings, func() time.Time { return now })
	a.Config.PersonalFixture = rotatedFixture
	a.Config.PersonalEntitlements = rotatedFixture.ReadEntitlement
	handler = a.Handler(nil)
	if read(replacement).Premium {
		t.Fatal("another lease enabled copied grants")
	}
	newGeneration := readGeneration(a.Config.Origin, newCapability)
	if newGeneration == generation || grant(a.Config.Origin, newCapability, oldRequest).Code != 404 {
		t.Fatal("stale generation accepted after rotation")
	}
	a.Config.PersonalFixture = fixture
	a.Config.PersonalEntitlements = fixture.ReadEntitlement
	handler = a.Handler(nil)
	if request(http.MethodGet, a.Config.Origin, capability, "").Code != 404 {
		t.Fatal("retired lease could rebind")
	}
	now = settings.Expires
	if read(replacement).Premium || grant(a.Config.Origin, capability, body(replacement, "premium")).Code != http.StatusNotFound {
		t.Fatal("expired fixture remained enabled")
	}
}

func TestPersonalFixtureRouteIsAbsentFromNormalAPI(t *testing.T) {
	a, handler := testAPI(t)
	if a.Config.PersonalEntitlements != nil || a.Config.PersonalFixture != nil {
		t.Fatal("normal API has fixture permissions")
	}
	got := call(handler, http.MethodPost, personalFixturePath, `{"state":"premium"}`, "", "", a.Config.Origin)
	if got.Code != http.StatusNotFound {
		t.Fatal("normal runtime exposed a fixture-grant route", got.Code)
	}
}
