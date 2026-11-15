package billing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPlayVerifiesMatchedFundedPeriodAndSeparatesInitialSale(t *testing.T) {
	const app = "com.example.fixture"
	const token = "local-fixture-token"
	const order = "GPA.1234-5678-9012-34567"
	const binding = "5095aae8-27e3-45de-bef1-bf9b179cedcf"
	const purchase = `{"kind":"androidpublisher#subscriptionPurchaseV2","regionCode":"BE","subscriptionState":"SUBSCRIPTION_STATE_ACTIVE",
"testPurchase":{},"externalAccountIdentifiers":{"obfuscatedExternalAccountId":"` + binding + `"},"lineItems":[
{"productId":"fixture-premium","expiryTime":"2026-12-02T12:00:00Z","latestSuccessfulOrderId":"` + order + `",
"autoRenewingPlan":{"autoRenewEnabled":true},"offerDetails":{"basePlanId":"monthly"},"offerPhase":{"basePrice":{}}}]}`
	const payment = `{"orderId":"` + order + `","purchaseToken":"` + token + `","state":"PROCESSED",
"total":{"currencyCode":"EUR","units":"6","nanos":990000000},"lineItems":[{"productId":"fixture-premium",
"total":{"currencyCode":"EUR","units":"6","nanos":990000000},"subscriptionDetails":{
"basePlanId":"monthly","offerPhaseDetails":{"baseDetails":{}},"servicePeriodStartTime":"2026-11-01T12:00:00Z",
"servicePeriodEndTime":"2026-12-01T12:00:00Z"}}]}`
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name, sub, order string
		existing         bool
		want             error
	}{
		{"matched Belgian order supports checkout", purchase, payment, false, nil},
		{"existing international customer", strings.Replace(purchase, `"BE"`, `"FR"`, 1), strings.ReplaceAll(payment, `"EUR"`, `"USD"`), true, nil},
		{"wrong initial region", strings.Replace(purchase, `"BE"`, `"FR"`, 1), payment, false, ErrPurchaseBinding},
		{"wrong gross order amount", purchase, strings.ReplaceAll(payment, "990000000", "980000000"), false, ErrPurchaseBinding},
		{"foreign order token", purchase, strings.Replace(payment, token, "foreign-token", 1), false, ErrPurchaseBinding},
		{"wrong base plan", purchase, strings.Replace(payment, `"monthly"`, `"annual"`, 1), false, ErrPurchaseBinding},
		{"unexpected trial", strings.Replace(purchase, `"basePrice"`, `"freeTrial"`, 1), payment, false, ErrPurchaseBinding},
		{"discounted order phase", purchase, strings.Replace(payment, `"baseDetails"`, `"introductoryPriceDetails"`, 1), false, ErrPurchaseBinding},
		{"live proof rejected in sandbox", strings.Replace(purchase, `"testPurchase":{},`, "", 1), payment, false, ErrPurchaseBinding},
		{"new linked token waits for lineage reconciliation", strings.Replace(purchase, `"testPurchase":{},`, `"testPurchase":{},"linkedPurchaseToken":"previous-token",`, 1), payment, true, ErrUnresolved},
		{"pending is not a sale", strings.Replace(purchase, "STATE_ACTIVE", "STATE_PENDING", 1), payment, false, ErrUnresolved},
		{"checkout cannot use an unpaid order", purchase, strings.Replace(payment, "PROCESSED", "PENDING", 1), false, ErrUnresolved},
		{"grace never extends the funded period", strings.Replace(purchase, "STATE_ACTIVE", "STATE_IN_GRACE_PERIOD", 1), payment, true, nil},
		{"refund alone is not revocation", purchase, strings.Replace(payment, "PROCESSED", "REFUNDED", 1), true, nil},
		{"early expiry revokes the paid period", strings.ReplaceAll(strings.Replace(purchase, "STATE_ACTIVE", "STATE_EXPIRED", 1), "2026-12-02", "2026-11-12"), payment, true, nil},
		{"expired old token during replacement is not a revocation", strings.ReplaceAll(strings.Replace(purchase, `"subscriptionState":"SUBSCRIPTION_STATE_ACTIVE",`, `"subscriptionState":"SUBSCRIPTION_STATE_EXPIRED","canceledStateContext":{"replacementCancellation":{}},`, 1), "2026-12-02", "2026-11-12"), payment, true, ErrUnresolved},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: playTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Scheme != "https" || r.URL.Host != "androidpublisher.googleapis.com" ||
					r.Header.Get("Authorization") != "Bearer offline-fixture-access" || r.Method != http.MethodGet {
					t.Fatal("proof was routed outside the configured Play API")
				}
				base := "/androidpublisher/v3/applications/" + app
				switch r.URL.Path {
				case base + "/purchases/subscriptionsv2/tokens/" + token:
					return playTestReply(200, test.sub), nil
				case base + "/orders/" + order:
					return playTestReply(200, test.order), nil
				default:
					return nil, errors.New("unexpected private endpoint")
				}
			})}
			adapter, err := NewPlayProvider(PlayConfig{Package: app, Product: "fixture-premium",
				MonthlyBasePlan: "monthly", AnnualBasePlan: "annual", Environment: Sandbox,
				AccessToken: func(context.Context) (string, error) { return "offline-fixture-access", nil }}, client, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			lookup := adapter.Verify
			if test.existing {
				lookup = adapter.VerifyExisting
			}
			result, err := lookup(t.Context(), token)
			if !errors.Is(err, test.want) {
				t.Fatalf("verification returned %v, want %v", err, test.want)
			}
			if err != nil {
				return
			}
			wantPaid := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
			if calls != 2 || result.App != app || result.BasePlan != "monthly" || result.Period.Transaction != order ||
				result.Period.Binding != binding || result.Period.Subscription == token || result.Period.Subscription == "" ||
				!result.Period.PaidThrough.Equal(wantPaid) || result.Period.Revoked != (test.name == "early expiry revokes the paid period") {
				t.Fatal("accepted facts did not match the paid order")
			}
		})
	}
}

type playTestTransport func(*http.Request) (*http.Response, error)

func (f playTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func playTestReply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestPlayProofNeverFollowsADifferentOrigin(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: playTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "androidpublisher.googleapis.com" {
			t.Fatal("followed an unapproved redirect")
		}
		res := playTestReply(http.StatusTemporaryRedirect, "")
		res.Header.Set("Location", "https://unapproved.example.test/proof")
		return res, nil
	})}
	adapter, err := NewPlayProvider(PlayConfig{Package: "com.example.fixture", Product: "premium",
		MonthlyBasePlan: "monthly", AnnualBasePlan: "annual", Environment: Sandbox,
		AccessToken: func(context.Context) (string, error) { return "offline-fixture-access", nil }}, client, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Verify(t.Context(), "../unapproved-path"); !errors.Is(err, ErrPurchaseBinding) || calls != 0 {
		t.Fatal("an invalid token reached the provider")
	}
	if _, err := adapter.Verify(t.Context(), "local-fixture-token"); !errors.Is(err, ErrStoreUnavailable) || calls != 1 {
		t.Fatal("a redirect did not fail closed")
	}
}
