package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

const personalFixturePath = "/api/v1/fixture/personal-recipes/entitlement"

func TestPersonalFixturePermissionsRequireCapabilityAndSyntheticIncarnation(t *testing.T) {
	a, _ := testAPI(t)
	now := time.Now().UTC()
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
	grant := func(origin, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, personalFixturePath, strings.NewReader(body))
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
	body := func(instance, state string) string {
		data, err := json.Marshal(map[string]string{"accountId": "1", "accountInstance": instance, "state": state})
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
		if state == "premium" && (!value.Premium || value.ExpiresUnix != settings.Expires.Unix()) || state == "expired" && (!value.Premium || value.ExpiresUnix >= now.Unix()) || state == "free" && value.Premium {
			t.Fatal("fixture state mismatch", state)
		}
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
