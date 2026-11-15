package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/billing"
)

type billingVerifier func(context.Context, string) (billing.ProviderProof, error)

func (verify billingVerifier) Verify(ctx context.Context, receipt string) (billing.ProviderProof, error) {
	return verify(ctx, receipt)
}

func (verify billingVerifier) VerifyExisting(ctx context.Context, receipt string) (billing.ProviderProof, error) {
	return verify(ctx, receipt)
}

func TestNativeBillingCheckoutUsesVerifiedSessionPlatformAndFamily(t *testing.T) {
	a, _, _, _ := lifecycleHTTPFixture(t)
	h := a.Handler(nil)
	signed := nativeFixtureLogin(t, a, h)
	const start = "/billing/checkouts"
	const status = "/billing/status"
	if w := callNative(h, "POST", start, `{"interval":"monthly"}`, signed.AccessToken); w.Code != 404 {
		t.Fatalf("billing not configured: got %d", w.Code)
	}
	journal, err := a.Config.Lifecycle.Billing(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var binding string
	verifierCalls := 0
	verifier := billingVerifier(func(_ context.Context, receipt string) (billing.ProviderProof, error) {
		verifierCalls++
		if receipt != "synthetic-purchase-proof" {
			return billing.ProviderProof{}, errors.New("private provider diagnostic")
		}
		return billing.ProviderProof{
			App: "com.example.preview", Product: "monthly", Region: "BEL",
			Period: billing.VerifiedPeriod{Store: billing.Apple, Environment: billing.Sandbox,
				Binding: binding, Subscription: "sandbox-lineage", Transaction: "sandbox-order",
				Interval: billing.Monthly, StartsAt: now, PaidThrough: now.Add(time.Hour), ObservedAt: now},
		}, nil
	})
	offer := billing.Offer{App: "com.example.preview", Product: "monthly", Interval: billing.Monthly, Store: billing.Apple, Environment: billing.Sandbox}
	a.Config.NativeBilling = &NativeBilling{Journal: journal, Environment: billing.Sandbox, CheckoutEnabled: true,
		Offers: []billing.Offer{offer}, Providers: map[billing.Store]billing.Provider{billing.Apple: verifier}}
	h = a.Handler(nil)
	assert := func(want int, method, path, data, access string) *strings.Reader {
		t.Helper()
		w := callNative(h, method, path, data, access)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d (%s)", method, path, w.Code, want, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private provider diagnostic") {
			t.Fatal("response disclosed provider diagnostics")
		}
		return strings.NewReader(w.Body.String())
	}
	assert(http.StatusUnauthorized, "POST", start, `{"interval":"monthly"}`, "")
	assert(http.StatusUnauthorized, "GET", status, "", "")
	assert(http.StatusForbidden, "GET", status, "", signed.AccessToken)
	assert(http.StatusForbidden, "POST", start, `{"interval":"monthly"}`, signed.AccessToken)
	const restore = "/billing/restore"
	const receipt = `{"receipt":"synthetic-purchase-proof"}`
	assert(http.StatusUnauthorized, "POST", restore, receipt, "")
	assert(http.StatusForbidden, "POST", restore, receipt, signed.AccessToken)
	if verifierCalls != 0 {
		t.Fatal("unauthenticated or unverified restore contacted the provider")
	}
	if err := a.Store.DB.Exec("UPDATE users SET email_verified_at=? WHERE email=?", now.Unix(), "native-http@example.test").Error; err != nil {
		t.Fatal(err)
	}
	assert(http.StatusBadRequest, "POST", start, `{"interval":"monthly","store":"play"}`, signed.AccessToken)
	var checkout struct{ ID, Binding, Store string }
	if err := json.NewDecoder(assert(http.StatusCreated, "POST", start, `{"interval":"monthly"}`, signed.AccessToken)).Decode(&checkout); err != nil ||
		checkout.ID == "" || checkout.Binding == "" || checkout.Store != "apple" {
		t.Fatal("missing server-issued purchase binding")
	}
	firstStatus := callNative(h, "GET", status, "", signed.AccessToken)
	if firstStatus.Code != 200 || !strings.Contains(firstStatus.Body.String(), checkout.ID) ||
		strings.Contains(firstStatus.Body.String(), checkout.Binding) || firstStatus.Header().Get("X-MyChef-User") == "" {
		t.Fatal("the owning native installation did not get its resumable reservation")
	}
	binding = checkout.Binding
	// A lost reservation response cannot strand the verified installation.
	var replay struct{ ID, Binding, Store string }
	if err := json.NewDecoder(assert(http.StatusCreated, "POST", start, `{"interval":"monthly"}`, signed.AccessToken)).Decode(&replay); err != nil || replay != checkout {
		t.Fatal("reservation retry issued a different binding", err)
	}
	anonymous := nativeReply(t, callNative(h, "POST", "/session", `{"platform":"ios"}`, ""), 201)
	sibling := nativeReply(t, callNative(h, "POST", "/login", `{"email":"native-http@example.test","password":"Garden native 2026!"}`, anonymous.AccessToken), 200)
	siblingStatus := callNative(h, "GET", status, "", sibling.AccessToken)
	if siblingStatus.Code != 200 || !strings.Contains(siblingStatus.Body.String(), `"pending":true`) ||
		strings.Contains(siblingStatus.Body.String(), checkout.ID) || strings.Contains(siblingStatus.Body.String(), checkout.Binding) {
		t.Fatal("a sibling installation read the resumable checkout handle")
	}
	at := start + "/" + checkout.ID
	assert(http.StatusNotFound, "POST", at+"/launch", "", sibling.AccessToken)
	assert(http.StatusConflict, "POST", start, `{"interval":"monthly"}`, sibling.AccessToken)
	assert(http.StatusNotFound, "DELETE", at, "", sibling.AccessToken)
	assert(http.StatusBadRequest, "DELETE", at, `{"terminal":true}`, signed.AccessToken)
	assert(http.StatusNoContent, "DELETE", at, "", signed.AccessToken)
	assert(http.StatusNotFound, "POST", at+"/launch", "", signed.AccessToken)
	var replacement struct{ ID, Binding, Store string }
	if err := json.NewDecoder(assert(http.StatusCreated, "POST", start, `{"interval":"monthly"}`, signed.AccessToken)).Decode(&replacement); err != nil ||
		replacement.ID == "" || replacement.ID == checkout.ID || replacement.Binding == "" || replacement.Binding == checkout.Binding {
		t.Fatal("unused checkout was not released for a new reservation", err)
	}
	at = start + "/" + replacement.ID
	raceAt := at
	ready := make(chan struct{})
	launched, released := make(chan int, 1), make(chan int, 1)
	go func() {
		<-ready
		launched <- callNative(h, "POST", raceAt+"/launch", "", signed.AccessToken).Code
	}()
	go func() {
		<-ready
		released <- callNative(h, "DELETE", raceAt, "", signed.AccessToken).Code
	}()
	close(ready)
	launchCode, releaseCode := <-launched, <-released
	switch {
	case launchCode == http.StatusNoContent && releaseCode == http.StatusConflict:
		// The device may open the sheet; the original reservation still owns it.
	case releaseCode == http.StatusNoContent && (launchCode == http.StatusNotFound || launchCode == http.StatusConflict):
		// A losing launch must never authorize the abandoned sheet.
		var retry struct{ ID, Binding, Store string }
		if err := json.NewDecoder(assert(http.StatusCreated, "POST", start, `{"interval":"monthly"}`, signed.AccessToken)).Decode(&retry); err != nil ||
			retry.ID == "" || retry.ID == replacement.ID || retry.Binding == "" || retry.Binding == replacement.Binding {
			t.Fatal("release won the race but the abandoned reservation still blocked checkout", err)
		}
		replacement = retry
		at = start + "/" + replacement.ID
		assert(http.StatusNoContent, "POST", at+"/launch", "", signed.AccessToken)
	default:
		t.Fatalf("concurrent launch and unused release did not have exactly one winner: launch %d, release %d", launchCode, releaseCode)
	}
	binding = replacement.Binding
	assert(http.StatusConflict, "DELETE", at, "", signed.AccessToken)
	assert(http.StatusConflict, "POST", at+"/launch", "", signed.AccessToken)
	assert(http.StatusConflict, "POST", start, `{"interval":"monthly"}`, signed.AccessToken)
	resolve := at + "/verify"
	assert(http.StatusNotFound, "POST", resolve, `{"receipt":"synthetic-purchase-proof"}`, sibling.AccessToken)
	assert(http.StatusServiceUnavailable, "POST", resolve, `{"receipt":"sandbox-timeout"}`, signed.AccessToken)
	for range 2 {
		var result map[string]bool
		if err := json.NewDecoder(assert(http.StatusOK, "POST", resolve, `{"receipt":"synthetic-purchase-proof"}`, signed.AccessToken)).Decode(&result); err != nil ||
			!result["completeStoreTransaction"] {
			t.Fatalf("store completion not confirmed: %v", err)
		}
	}
	if verifierCalls != 3 {
		t.Fatalf("provider verification ran %d times", verifierCalls)
	}
	a.Config.NativeBilling.CheckoutEnabled = false
	disabled := callNative(h, "GET", status, "", signed.AccessToken)
	if disabled.Code != 200 || !strings.Contains(disabled.Body.String(), `"checkoutEnabled":false`) ||
		!strings.Contains(disabled.Body.String(), `"restoreAvailable":true`) ||
		!strings.Contains(disabled.Body.String(), `"paidThrough":`) ||
		strings.Contains(disabled.Body.String(), "sandbox-order") || strings.Contains(disabled.Body.String(), "sandbox-lineage") {
		t.Fatal("sales-off status concealed paid access or exposed provider data")
	}
	assert(http.StatusNotFound, "POST", start, `{"interval":"monthly"}`, signed.AccessToken)
	assert(http.StatusOK, "POST", resolve, `{"receipt":"synthetic-purchase-proof"}`, signed.AccessToken)
	assert(http.StatusBadRequest, "POST", restore, `{"receipt":"synthetic-purchase-proof","store":"play"}`, sibling.AccessToken)
	var restored map[string]bool
	if err := json.NewDecoder(assert(http.StatusOK, "POST", restore, receipt, sibling.AccessToken)).Decode(&restored); err != nil ||
		!restored["completeStoreTransaction"] {
		t.Fatal("the verified owner could not restore on their second installation with sales off", err)
	}
}

func TestNativeBillingRechecksAuthorityAfterProviderReturns(t *testing.T) {
	a, _, _, _ := lifecycleHTTPFixture(t)
	h := a.Handler(nil)
	signed := nativeFixtureLogin(t, a, h)
	now := time.Now()
	if err := a.Store.DB.Exec("UPDATE users SET email_verified_at=? WHERE email=?", now.Unix(), "native-http@example.test").Error; err != nil {
		t.Fatal(err)
	}
	journal, err := a.Config.Lifecycle.Billing(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var attempt struct{ ID, Binding string }
	verifier := billingVerifier(func(_ context.Context, _ string) (billing.ProviderProof, error) {
		// Emulate an account write while the provider request is in flight.
		// It must not wait for a mutex held by checkout verification.
		if !a.Store.AccountWork.TryLock() {
			t.Fatal("provider I/O holds the account lifecycle lock")
		}
		defer a.Store.AccountWork.Unlock()
		if err := a.Store.DB.Exec("UPDATE users SET email_verified_at=NULL WHERE email=?", "native-http@example.test").Error; err != nil {
			t.Fatal(err)
		}
		return billing.ProviderProof{App: "com.example.preview", Product: "monthly", Region: "BEL",
			Period: billing.VerifiedPeriod{Store: billing.Apple, Environment: billing.Sandbox, Binding: attempt.Binding,
				Subscription: "delayed-lineage", Transaction: "delayed-order", Interval: billing.Monthly,
				StartsAt: now, PaidThrough: now.Add(time.Hour), ObservedAt: now}}, nil
	})
	a.Config.NativeBilling = &NativeBilling{Journal: journal, Environment: billing.Sandbox, CheckoutEnabled: true,
		Offers: []billing.Offer{{App: "com.example.preview", Product: "monthly", Interval: billing.Monthly,
			Store: billing.Apple, Environment: billing.Sandbox}}, Providers: map[billing.Store]billing.Provider{billing.Apple: verifier}}
	h = a.Handler(nil)
	base := "/billing/checkouts"
	w := callNative(h, "POST", base, `{"interval":"monthly"}`, signed.AccessToken)
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &attempt) != nil || attempt.ID == "" {
		t.Fatalf("checkout reservation failed: %d", w.Code)
	}
	if w := callNative(h, "POST", base+"/"+attempt.ID+"/launch", "", signed.AccessToken); w.Code != 204 {
		t.Fatalf("checkout launch failed: %d", w.Code)
	}
	w = callNative(h, "POST", base+"/"+attempt.ID+"/verify", `{"receipt":"synthetic"}`, signed.AccessToken)
	if w.Code != 403 || strings.Contains(w.Body.String(), "completeStoreTransaction") {
		t.Fatalf("checkout completed after verification was lost: %d", w.Code)
	}
	var incarnation string
	if err := a.Store.SQL.QueryRow("SELECT a.incarnation FROM account_incarnations a JOIN users u ON u.user_id=a.user_id WHERE email=?", "native-http@example.test").Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	if active, err := journal.Active(t.Context(), incarnation, billing.Sandbox, now); err != nil || active != nil {
		t.Fatal("provider result granted access after verification was lost", err)
	}
}

func TestNativeBillingSandboxExpiresDuringVerificationAndRestore(t *testing.T) {
	for _, restore := range []bool{false, true} {
		label := "checkout"
		if restore {
			label = "restore"
		}
		t.Run(label, func(t *testing.T) {
			a, _, _, _ := lifecycleHTTPFixture(t)
			signed := nativeFixtureLogin(t, a, a.Handler(nil))
			const email = "native-http@example.test"
			now := time.Now()
			if err := a.Store.DB.Exec("UPDATE users SET email_verified_at=? WHERE email=?", now.Unix(), email).Error; err != nil {
				t.Fatal(err)
			}
			journal, err := a.Config.Lifecycle.Billing(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var attempt struct{ ID, Binding string }
			var active atomic.Bool
			active.Store(true)
			verifier := billingVerifier(func(_ context.Context, _ string) (billing.ProviderProof, error) {
				active.Store(false)
				return billing.ProviderProof{App: "com.example.preview", Product: "monthly", Region: "BEL",
					Period: billing.VerifiedPeriod{Store: billing.Apple, Environment: billing.Sandbox, Binding: attempt.Binding,
						Subscription: "delayed-lineage", Transaction: "delayed-order", Interval: billing.Monthly,
						StartsAt: now, PaidThrough: now.Add(time.Hour), ObservedAt: now}}, nil
			})
			a.Config.NativeBilling = &NativeBilling{Journal: journal, Environment: billing.Sandbox, CheckoutEnabled: true,
				SyntheticAccountsOnly: true, Available: active.Load,
				Offers:    []billing.Offer{{App: "com.example.preview", Product: "monthly", Interval: billing.Monthly, Store: billing.Apple, Environment: billing.Sandbox}},
				Providers: map[billing.Store]billing.Provider{billing.Apple: verifier}}
			h := a.Handler(nil)
			base := "/billing/checkouts"
			res := callNative(h, "POST", base, `{"interval":"monthly"}`, signed.AccessToken)
			if res.Code != 201 || json.Unmarshal(res.Body.Bytes(), &attempt) != nil || attempt.ID == "" {
				t.Fatalf("reservation failed: %d", res.Code)
			}
			if res = callNative(h, "POST", base+"/"+attempt.ID+"/launch", "", signed.AccessToken); res.Code != 204 {
				t.Fatalf("launch failed: %d", res.Code)
			}
			route := base + "/" + attempt.ID + "/verify"
			if restore {
				route = "/billing/restore"
			}
			res = callNative(h, "POST", route, `{"receipt":"synthetic"}`, signed.AccessToken)
			if res.Code != 404 || strings.Contains(res.Body.String(), "completeStoreTransaction") {
				t.Fatalf("expired Sandbox returned a fulfilment response: %d", res.Code)
			}
			var owner string
			if err := a.Store.SQL.QueryRow("SELECT a.incarnation FROM account_incarnations a JOIN users u ON u.user_id=a.user_id WHERE email=?", email).Scan(&owner); err != nil {
				t.Fatal(err)
			}
			if paid, err := journal.Active(t.Context(), owner, billing.Sandbox, now); err != nil || paid != nil {
				t.Fatal("the response created a paid period after Sandbox expiry", err)
			}
		})
	}
}

func TestNativeBillingAndroidRequiresProtectedPollRegistration(t *testing.T) {
	a, _, _, _ := lifecycleHTTPFixture(t)
	h := a.Handler(nil)
	nativeFixtureLogin(t, a, h)
	now := time.Now()
	if err := a.Store.DB.Exec("UPDATE users SET email_verified_at=? WHERE email=?", now.Unix(), "native-http@example.test").Error; err != nil {
		t.Fatal(err)
	}
	anon := nativeReply(t, callNative(h, "POST", "/session", `{"platform":"android"}`, ""), 201)
	signed := nativeReply(t, callNative(h, "POST", "/login", `{"email":"native-http@example.test","password":"Garden native 2026!"}`, anon.AccessToken), 200)
	journal, err := a.Config.Lifecycle.Billing(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const receipt = "synthetic-play-token"
	digest := sha256.Sum256([]byte("mychef/play/token/v1\x00" + receipt))
	offer := billing.Offer{App: "com.example.preview", Product: "premium", BasePlan: "monthly", Interval: billing.Monthly,
		Store: billing.Play, Environment: billing.Sandbox}
	var checkout struct{ ID, Binding string }
	calls := 0
	provider := billingVerifier(func(_ context.Context, trial string) (billing.ProviderProof, error) {
		calls++
		if trial != receipt {
			t.Fatal("unexpected proof")
		}
		return billing.ProviderProof{App: offer.App, Product: offer.Product, BasePlan: offer.BasePlan, Region: "BE",
			Period: billing.VerifiedPeriod{Store: billing.Play, Environment: billing.Sandbox,
				Binding: checkout.Binding, Subscription: hex.EncodeToString(digest[:]), Transaction: "GPA.1234-5678-9012-34567",
				Interval: billing.Monthly, StartsAt: now, PaidThrough: now.Add(time.Hour), ObservedAt: now}}, nil
	})
	cfg := &NativeBilling{Journal: journal, Environment: billing.Sandbox, CheckoutEnabled: true,
		Offers: []billing.Offer{offer}, Providers: map[billing.Store]billing.Provider{billing.Play: provider}}
	a.Config.NativeBilling = cfg
	h = a.Handler(nil)
	call := func(method, path, body string, want int) string {
		t.Helper()
		w := callNative(h, method, path, body, signed.AccessToken)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d", method, path, w.Code, want)
		}
		return w.Body.String()
	}
	status := call("GET", "/billing/status", "", 200)
	if !strings.Contains(status, `"checkoutEnabled":false`) || !strings.Contains(status, `"restoreAvailable":false`) {
		t.Fatal("plain provider was allowed to bypass the protected poll")
	}
	call("POST", "/billing/checkouts", `{"interval":"monthly"}`, 404)
	call("POST", "/billing/restore", `{"receipt":"synthetic-play-token"}`, 404)
	if calls != 0 {
		t.Fatal("incomplete configuration contacted the store")
	}
	cfg.Play, err = billing.NewPlayInbox(journal, cfg.Environment, cfg.Offers, provider, bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	status = call("GET", "/billing/status", "", 200)
	if !strings.Contains(status, `"checkoutEnabled":false`) || !strings.Contains(status, `"restoreAvailable":true`) {
		t.Fatal("configuring recovery incorrectly enabled Play checkout")
	}
	call("POST", "/billing/checkouts", `{"interval":"monthly"}`, 404)
	cfg.PlayCheckoutEnabled = true
	data := call("POST", "/billing/checkouts", `{"interval":"monthly"}`, 201)
	if err := json.Unmarshal([]byte(data), &checkout); err != nil || checkout.ID == "" || checkout.Binding == "" {
		t.Fatal("protected checkout was unavailable", err)
	}
	cfg.PlayCheckoutEnabled = false
	call("POST", "/billing/checkouts/"+checkout.ID+"/launch", "", 404)
	cfg.PlayCheckoutEnabled = true
	call("POST", "/billing/checkouts/"+checkout.ID+"/launch", "", 204)
	cfg.PlayCheckoutEnabled = false
	result := call("POST", "/billing/checkouts/"+checkout.ID+"/verify", `{"receipt":"synthetic-play-token"}`, 200)
	if !strings.Contains(result, `"completeStoreTransaction":true`) || calls != 1 || strings.Contains(result, receipt) {
		t.Fatal("receipt was not committed or remained in the response")
	}
	cfg.CheckoutEnabled = false
	call("POST", "/billing/restore", `{"receipt":"synthetic-play-token"}`, 200)
}

func TestNativeBillingSandboxLimitsRoutesToSyntheticAccounts(t *testing.T) {
	a, _, _, _ := lifecycleHTTPFixture(t)
	journal, err := a.Config.Lifecycle.Billing(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	a.Config.NativeBilling = &NativeBilling{Journal: journal, Environment: billing.Sandbox, CheckoutEnabled: true,
		SyntheticAccountsOnly: true, Providers: map[billing.Store]billing.Provider{billing.Apple: billingVerifier(func(context.Context, string) (billing.ProviderProof, error) {
			calls++
			return billing.ProviderProof{}, billing.ErrUnresolved
		})}, Offers: []billing.Offer{{Store: billing.Apple, Environment: billing.Sandbox, Interval: billing.Monthly,
			App: "com.example.fixture", Product: "fixture.monthly"}}}
	h := a.Handler(nil)
	for _, fixture := range []struct {
		email, name string
		allowed     bool
	}{{"synthetic-billing@example.test", "synthetic", true}, {"ordinary-billing@example.com", "ordinary", false}} {
		t.Run(fixture.name, func(t *testing.T) {
			const password = "A disposable verification phrase!"
			owner, err := a.Auth.Register(t.Context(), auth.Registration{Email: fixture.email, Password: password,
				Username: fixture.name, FirstName: "Billing", LastName: "Fixture"})
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Store.DB.Exec("UPDATE users SET email_verified_at=? WHERE user_id=?", time.Now().Unix(), owner.ID).Error; err != nil {
				t.Fatal(err)
			}
			anon := nativeReply(t, callNative(h, "POST", "/session", `{"platform":"ios"}`, ""), 201)
			signed := nativeReply(t, callNative(h, "POST", "/login", `{"email":"`+fixture.email+`","password":"`+password+`"}`, anon.AccessToken), 200)
			w := callNative(h, "GET", "/billing/status", "", signed.AccessToken)
			if fixture.allowed {
				if w.Code != 200 || !strings.Contains(w.Body.String(), `"checkoutEnabled":true`) {
					t.Fatal("synthetic account could not see its sandbox checkout")
				}
				return
			}
			if w.Code != 404 || callNative(h, "POST", "/billing/restore", `{"receipt":"1234"}`, signed.AccessToken).Code != 404 ||
				callNative(h, "POST", "/billing/checkouts", `{"interval":"monthly"}`, signed.AccessToken).Code != 404 || calls != 0 {
				t.Fatal("ordinary verified account could enter the sandbox or contact the store")
			}
		})
	}
}

func TestNativeBillingRetainsSellingStoreAfterExpiryWithCheckoutOff(t *testing.T) {
	a, _, _, _ := lifecycleHTTPFixture(t)
	h := a.Handler(nil)
	account := nativeFixtureLogin(t, a, h)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	if err := a.Store.DB.Exec("UPDATE users SET email_verified_at=? WHERE email=?", now.Unix(), "native-http@example.test").Error; err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := a.Store.SQL.QueryRowContext(ctx, `SELECT i.incarnation FROM account_incarnations i JOIN users u USING(user_id) WHERE u.email=?`,
		"native-http@example.test").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	journal, err := a.Config.Lifecycle.Billing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	start, end := now.Add(-2*time.Hour), now.Add(-time.Hour)
	attempt, err := journal.Reserve(ctx, owner, billing.Sandbox, billing.Play, billing.Annual, start)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Launch(ctx, attempt, start); err != nil {
		t.Fatal(err)
	}
	period := billing.VerifiedPeriod{Store: billing.Play, Environment: billing.Sandbox, Interval: billing.Annual,
		Binding: attempt.Binding, Subscription: "disposable-expired-lineage", Transaction: "disposable-expired-transaction",
		StartsAt: start, PaidThrough: end, ObservedAt: now}
	if err := journal.Fulfil(ctx, period); err != nil {
		t.Fatal(err)
	}
	a.Config.NativeBilling = &NativeBilling{Journal: journal, Environment: billing.Sandbox}
	h = a.Handler(nil)
	w := callNative(h, "GET", "/billing/status", "", account.AccessToken)
	var status nativeBillingStatus
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.Store != billing.Apple || status.CheckoutEnabled ||
		status.Subscription != nil || status.Management == nil || status.Management.Store != billing.Play || status.Management.State != "ended" ||
		status.Management.EndedAt == nil || !status.Management.EndedAt.Equal(end) {
		t.Fatalf("sales-off Apple client could not manage its expired Play subscription: status %d", w.Code)
	}
	for _, fact := range []string{attempt.Binding, period.Subscription, period.Transaction} {
		if strings.Contains(w.Body.String(), fact) {
			t.Fatal("management response leaked private purchase references")
		}
	}
}
