package billing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPlaySandboxCatalogChecksConfiguredBelgianPlansAndOffers(t *testing.T) {
	const app = "com.example.fixture"
	const product = "fixture.premium"
	const catalog = `{"packageName":"com.example.fixture","productId":"fixture.premium","basePlans":[
	  {"basePlanId":"monthly","state":"ACTIVE","autoRenewingBasePlanType":{"billingPeriodDuration":"P1M","gracePeriodDuration":"P0D"},
	   "regionalConfigs":[{"regionCode":"BE","newSubscriberAvailability":true,"price":{"currencyCode":"EUR","units":"6","nanos":990000000}},{"regionCode":"FR"}]},
	  {"basePlanId":"annual","state":"ACTIVE","autoRenewingBasePlanType":{"billingPeriodDuration":"P1Y","gracePeriodDuration":"P0D",
	    "prorationMode":"SUBSCRIPTION_PRORATION_MODE_CHARGE_ON_NEXT_BILLING_DATE"},
	   "regionalConfigs":[{"regionCode":"BE","newSubscriberAvailability":true,"price":{"currencyCode":"EUR","units":"69","nanos":900000000}}]}]}`
	for _, tc := range []struct {
		name, catalog, offers string
		want                  error
	}{
		{"exact plans", catalog, `{}`, nil},
		{"archived discount only", catalog, `{"subscriptionOffers":[{"packageName":"com.example.fixture","productId":"fixture.premium","basePlanId":"monthly","offerId":"legacy","state":"INACTIVE"}]}`, nil},
		{"wrong app", strings.Replace(catalog, app, "com.example.other", 1), `{}`, ErrPurchaseBinding},
		{"wrong annual price", strings.Replace(catalog, "900000000", "990000000", 1), `{}`, ErrPurchaseBinding},
		{"wrong annual term", strings.Replace(catalog, "P1Y", "P6M", 1), `{}`, ErrPurchaseBinding},
		{"new regions enabled", strings.Replace(catalog, `"basePlanId":"monthly"`, `"basePlanId":"monthly","otherRegionsConfig":{"newSubscriberAvailability":true}`, 1), `{}`, ErrPurchaseBinding},
		{"France enabled", strings.Replace(catalog, `"regionCode":"FR"`, `"regionCode":"FR","newSubscriberAvailability":true`, 1), `{}`, ErrPurchaseBinding},
		{"extra active plan", strings.Replace(catalog, `"basePlans":[`, `"basePlans":[{"basePlanId":"other","state":"ACTIVE"},`, 1), `{}`, ErrPurchaseBinding},
		{"nonzero grace", strings.Replace(catalog, "P0D", "P7D", 1), `{}`, ErrPurchaseBinding},
		{"immediate charge", strings.Replace(catalog, "CHARGE_ON_NEXT_BILLING_DATE", "CHARGE_FULL_PRICE_IMMEDIATELY", 1), `{}`, ErrPurchaseBinding},
		{"active trial", catalog, `{"subscriptionOffers":[{"packageName":"com.example.fixture","productId":"fixture.premium","basePlanId":"monthly","offerId":"trial","state":"ACTIVE"}]}`, ErrPurchaseBinding},
		{"incomplete offer list", catalog, `{"nextPageToken":"more"}`, ErrStoreUnavailable},
		{"unreadable offer list", catalog, `null`, ErrStoreUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queries := 0
			client := &http.Client{Transport: playTestTransport(func(req *http.Request) (*http.Response, error) {
				queries++
				if req.Method != "GET" || req.URL.Host != "androidpublisher.googleapis.com" || req.Header.Get("Authorization") != "Bearer offline-test-token" {
					t.Fatal("catalog preflight went outside its authenticated GET scope")
				}
				prefix := "/androidpublisher/v3/applications/" + app + "/subscriptions/" + product
				if req.URL.Path == prefix && req.URL.RawQuery == "" {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.catalog))}, nil
				}
				if req.URL.Path == prefix+"/basePlans/-/offers" && req.URL.Query().Get("pageSize") == "100" {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.offers))}, nil
				}
				t.Fatal("unexpected catalog path")
				return nil, ErrStoreUnavailable
			})}
			provider, err := NewPlayProvider(PlayConfig{Package: app, Product: product, MonthlyBasePlan: "monthly", AnnualBasePlan: "annual",
				Environment: Sandbox, AccessToken: func(context.Context) (string, error) { return "offline-test-token", nil }}, client, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = provider.CheckSandboxCheckoutCatalog(t.Context())
			if !errors.Is(err, tc.want) {
				t.Fatalf("catalog result %v; want %v", err, tc.want)
			}
			if queries == 0 || queries > 2 {
				t.Fatalf("unexpected catalog request count: %d", queries)
			}
			provider.cfg.Environment = Production
			prior := queries
			if err = provider.CheckSandboxCheckoutCatalog(t.Context()); !errors.Is(err, ErrPurchaseBinding) || queries != prior {
				t.Fatal("catalog gate could be used to activate production checkout")
			}
		})
	}
}
