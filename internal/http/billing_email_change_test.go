package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/billing"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
)

func TestVerifiedEmailChangeKeepsBillingOwnershipAcrossClients(t *testing.T) {
	a, _, member, web := lifecycleHTTPFixture(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	if err := a.Store.DB.Model(&member).Update("email_verified_at", now.Unix()).Error; err != nil {
		t.Fatal(err)
	}
	keys, err := mailqueue.NewProofKeys("test", map[string][]byte{"test": bytes.Repeat([]byte{3}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	a.Config.EmailRateKey = bytes.Repeat([]byte{4}, 32)
	a.Config.EmailChange = &auth.EmailChange{SignUp: &auth.SignUp{Store: a.Store, Keys: keys,
		RateKey: a.Config.EmailRateKey, Origin: a.Config.Origin}}
	journal, err := a.Config.Lifecycle.Billing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const app, product, receipt = "com.example.preview", "premium-monthly", "offline-apple-proof"
	offer := billing.Offer{Store: billing.Apple, Environment: billing.Sandbox, Interval: billing.Monthly, App: app, Product: product}
	period := billing.VerifiedPeriod{Store: billing.Apple, Environment: billing.Sandbox, Interval: billing.Monthly,
		Subscription: "offline-apple-lineage", Transaction: "offline-apple-period", StartsAt: now,
		PaidThrough: now.Add(time.Hour), ObservedAt: now}
	a.Config.NativeBilling = &NativeBilling{Journal: journal, Environment: billing.Sandbox, CheckoutEnabled: true, Offers: []billing.Offer{offer},
		Providers: map[billing.Store]billing.Provider{billing.Apple: billingVerifier(func(_ context.Context, proof string) (billing.ProviderProof, error) {
			if proof != receipt || period.Binding == "" {
				return billing.ProviderProof{}, billing.ErrPurchaseBinding
			}
			return billing.ProviderProof{App: app, Product: product, Region: "BEL", Period: period}, nil
		})}}
	a.Config.PersonalEntitlements = a.Config.NativeBilling.Entitlements(nil)
	h := a.Handler(nil)
	loginBody := func(email string) string {
		return credentialJSON(t, map[string]string{"email": email, "password": lifecyclePassword})
	}
	nativeLogin := func(platform, email string) auth.NativeTokens {
		t.Helper()
		anonymous := nativeReply(t, callNative(h, "POST", "/session", credentialJSON(t, map[string]string{"platform": platform}), ""), http.StatusCreated)
		return nativeReply(t, callNative(h, "POST", "/login", loginBody(email), anonymous.AccessToken), http.StatusOK)
	}
	webLogin := func(email string) string {
		t.Helper()
		anonymous := sessionCookie(t, call(h, "GET", "/api/v1/session", "", "", "", ""))
		w := call(h, "POST", "/api/v1/login", loginBody(email), anonymous, auth.CSRF(anonymous), a.Config.Origin)
		if w.Code != http.StatusOK {
			t.Fatalf("web login returned %d", w.Code)
		}
		return sessionCookie(t, w)
	}
	oldIOS, oldAndroid := nativeLogin("ios", member.Email), nativeLogin("android", member.Email)
	const checkoutPath = "/billing/checkouts"
	w := callNative(h, "POST", checkoutPath, `{"interval":"monthly"}`, oldIOS.AccessToken)
	var checkout struct{ ID, Binding string }
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &checkout) != nil || checkout.ID == "" || checkout.Binding == "" {
		t.Fatalf("could not reserve original purchase: %d", w.Code)
	}
	period.Binding = checkout.Binding
	if w := callNative(h, "POST", checkoutPath+"/"+checkout.ID+"/launch", "", oldIOS.AccessToken); w.Code != http.StatusNoContent {
		t.Fatalf("could not launch original purchase: %d", w.Code)
	}
	proof := credentialJSON(t, map[string]string{"receipt": receipt})
	if w := callNative(h, "POST", checkoutPath+"/"+checkout.ID+"/verify", proof, oldIOS.AccessToken); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"completeStoreTransaction":true`) {
		t.Fatalf("could not fulfil original purchase: %d", w.Code)
	}
	const capability = "/personal-recipes/permissions"
	assertCapability := func(client string, status int, body []byte, want bool) {
		t.Helper()
		var got struct{ CanWrite bool }
		if status != http.StatusOK || json.Unmarshal(body, &got) != nil || got.CanWrite != want {
			t.Fatalf("%s capability: status %d, can write %t; want %t", client, status, got.CanWrite, want)
		}
	}
	assertAll := func(webToken, iosToken, androidToken string, want bool) {
		t.Helper()
		browser := call(h, "GET", "/api/v1"+capability, "", webToken, "", a.Config.Origin)
		assertCapability("web", browser.Code, browser.Body.Bytes(), want)
		for client, access := range map[string]string{"ios": iosToken, "android": androidToken} {
			w := callNative(h, "GET", capability, "", access)
			assertCapability(client, w.Code, w.Body.Bytes(), want)
			w = callNative(h, "GET", "/billing/status", "", access)
			var state nativeBillingStatus
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || (state.Subscription != nil) != want {
				t.Fatalf("%s did not see the same purchase: %d", client, w.Code)
			}
			if want && (state.Subscription.Store != billing.Apple || !state.Subscription.PaidThrough.Equal(period.PaidThrough)) {
				t.Fatalf("%s saw the wrong selling store or paid-through date", client)
			}
		}
	}
	assertAll(web, oldIOS.AccessToken, oldAndroid.AccessToken, true)
	const proposed = "billing-new-address@example.test"
	change := credentialJSON(t, map[string]string{"email": proposed, "currentPassword": lifecyclePassword})
	if w := call(h, "POST", "/api/v1/email/change", change, web, auth.CSRF(web), a.Config.Origin); w.Code != http.StatusAccepted {
		t.Fatalf("email change did not start: %d", w.Code)
	}
	for _, decision := range []struct{ template, field, status string }{
		{mailqueue.EmailChangeCurrentTemplate, "approve", "pending"},
		{mailqueue.EmailChangeProposedTemplate, "link", "changed"},
	} {
		var row mailqueue.Record
		if err := a.Store.DB.Where("template = ?", decision.template).First(&row).Error; err != nil {
			t.Fatal(err)
		}
		fields, _, err := mailqueue.OpenProtected(keys, row, time.Now(), false)
		if err != nil {
			t.Fatal(err)
		}
		link, err := url.Parse(fields[decision.field])
		if err != nil || link.Fragment == "" {
			t.Fatal("missing synthetic mailbox proof")
		}
		body := credentialJSON(t, map[string]string{"proof": link.Fragment})
		w := call(h, "POST", "/api/v1/email/change/decision", body, web, auth.CSRF(web), a.Config.Origin)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"`+decision.status+`"`) {
			t.Fatalf("email change did not reach %s: %d", decision.status, w.Code)
		}
	}
	if w := call(h, "GET", "/api/v1"+capability, "", web, "", a.Config.Origin); w.Code != http.StatusUnauthorized {
		t.Fatalf("stale web session retained access after email change: %d", w.Code)
	}
	for _, old := range []auth.NativeTokens{oldIOS, oldAndroid} {
		if w := callNative(h, "GET", "/billing/status", "", old.AccessToken); w.Code != http.StatusUnauthorized {
			t.Fatalf("old native access token survived email change: %d", w.Code)
		}
		body := credentialJSON(t, map[string]string{"refreshToken": old.RefreshToken})
		if w := callNative(h, "POST", "/refresh", body, ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("old native refresh token survived email change: %d", w.Code)
		}
	}
	newIOS, newAndroid := nativeLogin("ios", proposed), nativeLogin("android", proposed)
	assertAll(webLogin(proposed), newIOS.AccessToken, newAndroid.AccessToken, true)
	if w := callNative(h, "POST", "/billing/restore", proof, newIOS.AccessToken); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"completeStoreTransaction":true`) {
		t.Fatalf("renamed account could not restore its purchase: %d", w.Code)
	}
	unrelated, err := a.Auth.Register(ctx, auth.Registration{Email: member.Email, Password: lifecyclePassword,
		Username: "new-owner", FirstName: "New", LastName: "Fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.DB.Model(&unrelated).Update("email_verified_at", now.Unix()).Error; err != nil {
		t.Fatal(err)
	}
	other := nativeLogin("ios", member.Email)
	assertAll(webLogin(member.Email), other.AccessToken, nativeLogin("android", member.Email).AccessToken, false)
	if w := callNative(h, "POST", "/billing/restore", proof, other.AccessToken); w.Code != http.StatusNotFound {
		t.Fatalf("the previous address could claim a different incarnation's purchase: %d", w.Code)
	}
}
