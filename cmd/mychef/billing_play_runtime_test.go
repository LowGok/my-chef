package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/billing"
	"github.com/LowGok/my-chef/internal/config"
)

func TestPlaySandboxRequiresPrivateFixtureAndSeparateExpiringCredentials(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	origin := "https://fixture-node.tailtest.ts.net"
	cfg := config.Config{Secure: true, Provider: "fixture", Addr: "127.0.0.1:8090", Origin: origin,
		Database: filepath.Join(root, "app.db"), LifecycleDatabase: filepath.Join(root, "app.db.lifecycle")}
	inbox := &config.FixtureInbox{Origin: origin, Expires: now.Add(25 * time.Minute), CapabilityHash: [32]byte{1}}
	const publisher = "offline-fixture.android-publisher-authorization"
	const pubsub = "offline-fixture.pubsub-subscriber-authorization"
	expires := now.Add(21 * time.Minute)
	fields := map[string]any{"product": "fixture-premium", "monthly_base_plan": "monthly", "annual_base_plan": "annual",
		"project": "fixture-play", "subscription": "preview-notices",
		"publisher": map[string]any{"access_token": publisher, "expires_unix": expires.Unix()},
		"pubsub":    map[string]any{"access_token": pubsub, "expires_unix": now.Add(28 * time.Minute).Unix()},
		"inbox_key": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))}
	credential := filepath.Join(root, "play-sandbox.json")
	write := func() {
		t.Helper()
		raw, err := json.Marshal(fields)
		if err != nil || os.WriteFile(credential, raw, 0600) != nil {
			t.Fatal("synthetic credential setup failed")
		}
	}
	t.Setenv("MYCHEF_DEV", "1")
	t.Setenv("MYCHEF_E2E", "1")
	t.Setenv("MYCHEF_BILLING_PREVIEW_ROOT", root)
	t.Setenv("MYCHEF_PLAY_BILLING_SANDBOX_FILE", "")
	t.Setenv("MYCHEF_PLAY_BILLING_SANDBOX_CHECKOUT", "")
	if mode, err := loadPlayBillingSandbox(cfg, inbox); mode != nil || err != nil {
		t.Fatal("Play sandbox enabled by default", err)
	}
	t.Setenv("MYCHEF_PLAY_BILLING_SANDBOX_CHECKOUT", "1")
	if mode, err := loadPlayBillingSandbox(cfg, inbox); mode != nil || err == nil {
		t.Fatal("checkout could start without credentials", err)
	}
	t.Setenv("MYCHEF_PLAY_BILLING_SANDBOX_CHECKOUT", "0")
	t.Setenv("MYCHEF_PLAY_BILLING_SANDBOX_FILE", credential)
	write()
	for _, bad := range []config.Config{
		{Secure: true, Provider: "fixture", Addr: "0.0.0.0:8090", Origin: origin, Database: cfg.Database, LifecycleDatabase: cfg.LifecycleDatabase},
		{Secure: true, Provider: "fixture", Addr: cfg.Addr, Origin: "https://public.example.test", Database: cfg.Database, LifecycleDatabase: cfg.LifecycleDatabase},
		{Secure: true, Provider: "fixture", Addr: cfg.Addr, Origin: origin, Database: cfg.Database, LifecycleDatabase: filepath.Join(root, "other.db")},
	} {
		if mode, err := loadPlayBillingSandbox(bad, inbox); mode != nil || err == nil {
			t.Fatal("unbounded Preview configuration started Play", err)
		}
	}
	mode, err := loadPlayBillingSandbox(cfg, inbox)
	if mode == nil || err != nil || mode.checkout || !mode.expires.Equal(expires) {
		t.Fatal("isolated Sandbox defaults or deadlines are incorrect", err)
	}
	_, j := runtimeDatabase(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	publisherChecks, pubsubChecks := make(chan bool, 1), make(chan bool, 1)
	const fakeReceipt = "synthetic-play-proof-check"
	const catalogPath = "/androidpublisher/v3/applications/" + playPreviewPackage + "/subscriptions/fixture-premium"
	const approvedCatalog = `{"packageName":"` + playPreviewPackage + `","productId":"fixture-premium","basePlans":[
		{"basePlanId":"monthly","state":"ACTIVE","autoRenewingBasePlanType":{"billingPeriodDuration":"P1M","gracePeriodDuration":"P0D"},
		 "regionalConfigs":[{"regionCode":"BE","newSubscriberAvailability":true,"price":{"currencyCode":"EUR","units":"6","nanos":990000000}}]},
		{"basePlanId":"annual","state":"ACTIVE","autoRenewingBasePlanType":{"billingPeriodDuration":"P1Y","gracePeriodDuration":"P0D"},
		 "regionalConfigs":[{"regionCode":"BE","newSubscriberAvailability":true,"price":{"currencyCode":"EUR","units":"69","nanos":900000000}}]}]}`
	catalog := approvedCatalog
	catalogReads := 0
	client := &http.Client{Transport: appleFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "androidpublisher.googleapis.com" {
			if req.URL.Path == catalogPath || req.URL.Path == catalogPath+"/basePlans/-/offers" {
				catalogReads++
				if req.Method != "GET" || req.Header.Get("Authorization") != "Bearer "+publisher {
					t.Fatal("Sandbox catalog read used the wrong scope")
				}
				payload := "{}"
				if req.URL.Path == catalogPath {
					payload = catalog
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload))}, nil
			}
			select {
			case publisherChecks <- req.Method == "GET" &&
				req.URL.Path == "/androidpublisher/v3/applications/"+playPreviewPackage+"/purchases/subscriptionsv2/tokens/"+fakeReceipt &&
				req.Header.Get("Authorization") == "Bearer "+publisher:
			default:
			}
			// Refuse the proof after exercising the bound store connection.
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		select {
		case pubsubChecks <- req.Method == "POST" && req.URL.Host == "pubsub.googleapis.com" &&
			req.URL.Path == "/v1/projects/fixture-play/subscriptions/preview-notices:pull" &&
			req.Header.Get("Authorization") == "Bearer "+pubsub:
		default:
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	native, service, err := mode.start(ctx, j, client, nil)
	if err != nil || native == nil || service == nil || native.CheckoutEnabled || native.PlayCheckoutEnabled ||
		!native.SyntheticAccountsOnly || native.Environment != billing.Sandbox || native.Play == nil || !native.Available() {
		t.Fatal("default Play mode did not start with checkout disabled", err)
	}
	if catalogReads != 0 {
		t.Fatal("recovery-only Sandbox checked the sales catalog")
	}
	if len(native.Offers) != 2 || !native.Play.Offers(billing.Offer{App: playPreviewPackage, Product: "fixture-premium",
		BasePlan: "annual", Store: billing.Play, Environment: billing.Sandbox, Interval: billing.Annual}) {
		t.Fatal("protected Play worker did not share the configured offer")
	}
	if _, err := native.Play.Restore(ctx, strings.Repeat("a", 32), fakeReceipt, func(commit func() error) error { return commit() }); err == nil {
		t.Fatal("the offline fixture did not reject its unavailable Store proof")
	}
	select {
	case scoped := <-publisherChecks:
		if !scoped {
			t.Fatal("Play restore used the wrong package or bearer")
		}
	default:
		t.Fatal("Play restore never reached its protected provider")
	}
	done := make(chan struct{})
	go func() { defer close(done); service.Run(ctx) }()
	select {
	case scoped := <-pubsubChecks:
		if !scoped {
			t.Error("Play worker used the wrong source or bearer")
		}
	case <-time.After(3 * time.Second):
		t.Error("sandbox worker did not pull")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not stop the Play worker")
	}

	publisherFields := fields["publisher"].(map[string]any)
	publisherFields["access_token"] = pubsub
	write()
	if bad, err := loadPlayBillingSandbox(cfg, inbox); bad != nil || err == nil {
		t.Fatal("publisher and subscriber authority were mixed", err)
	}
	publisherFields["access_token"] = publisher
	publisherFields["expires_unix"] = now.Add(-time.Second).Unix()
	write()
	if bad, err := loadPlayBillingSandbox(cfg, inbox); bad != nil || err == nil {
		t.Fatal("expired publisher token accepted", err)
	}
	publisherFields["expires_unix"] = expires.Unix()
	fields["environment"] = "production"
	write()
	if bad, err := loadPlayBillingSandbox(cfg, inbox); bad != nil || err == nil {
		t.Fatal("configuration selected a non-Sandbox environment", err)
	}
	delete(fields, "environment")
	write()
	if err := os.Chmod(credential, 0644); err != nil {
		t.Fatal(err)
	}
	if bad, err := loadPlayBillingSandbox(cfg, inbox); bad != nil || err == nil {
		t.Fatal("group-readable credential accepted", err)
	}
	if err := os.Chmod(credential, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYCHEF_PLAY_BILLING_SANDBOX_CHECKOUT", "1")
	licensed, err := loadPlayBillingSandbox(cfg, inbox)
	if err != nil || licensed == nil || !licensed.checkout {
		t.Fatal("explicit testing stage could not be prepared offline", err)
	}
	catalog = strings.Replace(approvedCatalog, "900000000", "990000000", 1)
	var health billing.PlayWorkerHealth
	native, _, err = licensed.start(t.Context(), j, client, func(v billing.PlayWorkerHealth) { health = v })
	if err != nil || native == nil || !native.Available() || native.Play == nil || native.CheckoutEnabled || native.PlayCheckoutEnabled ||
		health.Stage != billing.PlayCatalogStage || !health.Degraded {
		t.Fatal("mismatched price did not block checkout and preserve recovery", err)
	}
	catalog = approvedCatalog
	native, _, err = licensed.start(t.Context(), j, client, func(v billing.PlayWorkerHealth) { health = v })
	if err != nil || native == nil || !native.CheckoutEnabled || !native.PlayCheckoutEnabled ||
		native.Environment != billing.Sandbox || !native.SyntheticAccountsOnly || catalogReads != 3 || health.Degraded {
		t.Fatal("explicit fixture checkout escaped licensed Sandbox limits", err)
	}
}
