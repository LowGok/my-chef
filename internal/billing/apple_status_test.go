package billing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestKnownApplePollRefreshesAnOwnedLineageWithoutNotifications(t *testing.T) {
	db, j, first := fixture(t)
	ctx := t.Context()
	a, err := j.Reserve(ctx, owner, Sandbox, Apple, Monthly, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Launch(ctx, a, first); err != nil {
		t.Fatal(err)
	}
	p := VerifiedPeriod{Store: Apple, Environment: Sandbox, Binding: a.Binding, Subscription: "123456", Transaction: "123456",
		Interval: Monthly, StartsAt: first, PaidThrough: first.Add(time.Hour), ObservedAt: first}
	if err := j.Fulfil(ctx, p); err != nil {
		t.Fatal(err)
	}
	now := p.PaidThrough
	roots, chain, signer := appleChainFixture(t, now, true)
	status, badBinding, transient := 1, true, true
	refCalls := 0
	client := &http.Client{Transport: appleTransport(func(req *http.Request) (*http.Response, error) {
		refCalls++
		if req.Method != "GET" || req.URL.String() != "https://api.storekit-sandbox.apple.com/inApps/v1/subscriptions/123456" ||
			!strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") {
			t.Fatal("poll did not query the exact known Apple lineage")
		}
		if transient {
			return &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		b := a.Binding
		if badBinding {
			b = "99887766-5544-4321-8123-4567890abc01"
		}
		data := map[string]any{"bundleId": "com.example.preview", "subscriptionGroupIdentifier": "group-1", "productId": "premium-yearly",
			"transactionId": "123457", "originalTransactionId": "123456", "appAccountToken": b, "environment": "Sandbox",
			"storefront": "USA", "type": "Auto-Renewable Subscription", "inAppOwnershipType": "PURCHASED", "price": 74900, "currency": "USD",
			"purchaseDate": p.PaidThrough.UnixMilli(), "expiresDate": p.PaidThrough.Add(time.Hour).UnixMilli(), "signedDate": now.UnixMilli()}
		if status == 5 {
			data["revocationDate"] = now.UnixMilli()
		}
		proof := appleSignFixture(t, map[string]any{"alg": "ES256", "x5c": chain}, data, signer)
		response := map[string]any{"environment": "Sandbox", "bundleId": "com.example.preview", "data": []any{
			map[string]any{"subscriptionGroupIdentifier": "group-1", "lastTransactions": []any{
				map[string]any{"originalTransactionId": "123456", "status": status, "signedTransactionInfo": proof},
				map[string]any{"originalTransactionId": "999999", "status": 1, "signedTransactionInfo": "unrelated"},
			}},
		}}
		raw, _ := json.Marshal(response)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	provider, err := NewAppleProvider(AppleConfig{BundleID: "com.example.preview", GroupID: "group-1", Environment: Sandbox,
		Issuer: "00000000-0000-0000-0000-000000000001", KeyID: "ABCDEFGHIJ", Key: signer, Roots: roots,
		Products: map[string]Interval{"premium-monthly": Monthly, "premium-yearly": Annual}}, client, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	reconciler := AppleReconciler{Journal: j, Environment: Sandbox, Now: func() time.Time { return now }, Provider: provider,
		Offers: []Offer{{Store: Apple, Environment: Sandbox, App: "com.example.preview", Product: "premium-yearly", Interval: Annual}},
		WithLiveOwner: func(_ context.Context, who string, commit func() error) error {
			if who != owner {
				t.Fatal("poll lost the owner")
			}
			return commit()
		}}
	// No fake push or notification is inserted for the renewal.
	check := func(want int) {
		t.Helper()
		got, err := reconciler.PollKnown(ctx)
		if err != nil || got.Processed != want {
			t.Fatal("unexpected poll count", got, err)
		}
	}
	assertTier := func(want bool) {
		t.Helper()
		period, err := j.Active(ctx, owner, Sandbox, now)
		if err != nil || (period != nil) != want {
			t.Fatal("unexpected Premium state", period, err)
		}
	}
	check(1)
	transient = false
	reconciler.Journal = NewJournal(db)
	check(0)
	assertTier(false)
	now = now.Add(time.Minute)
	check(1) // Different account binding on the exact store lineage must fail.
	assertTier(false)
	now = now.Add(2 * time.Minute)
	badBinding = false
	status = 4
	check(1) // A grace period cannot grant the reported future entitlement.
	assertTier(false)
	now = now.Add(4 * time.Minute)
	status = 1
	check(1)
	assertTier(true)
	check(0)
	now = now.Add(15 * time.Minute)
	status = 5
	check(1)
	assertTier(false)
	if refCalls != 5 {
		t.Fatal("polling ignored persisted retry or refresh times", refCalls)
	}
}
