package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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

func TestAppleSandboxRequiresPrivatePreviewAndAnOwnedCredential(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	origin := "https://fixture-node.tailtest.ts.net"
	cfg := config.Config{Secure: true, Provider: "fixture", Addr: "127.0.0.1:8090", Origin: origin,
		Database: filepath.Join(root, "app.db"), LifecycleDatabase: filepath.Join(root, "app.db.lifecycle")}
	inbox := &config.FixtureInbox{Origin: origin, Expires: time.Now().Add(5 * time.Minute), CapabilityHash: [32]byte{1}}
	t.Setenv("MYCHEF_DEV", "1")
	t.Setenv("MYCHEF_E2E", "1")
	t.Setenv("MYCHEF_BILLING_PREVIEW_ROOT", root)
	t.Setenv("MYCHEF_APPLE_BILLING_SANDBOX_FILE", "")
	if mode, err := loadAppleBillingSandbox(cfg, inbox); mode != nil || err != nil {
		t.Fatal("application would enable Apple billing by default", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{"issuer_id": "5095aae8-27e3-45de-bef1-bf9b179cedcf", "key_id": "FIXTUREKEY",
		"app_apple_id": int64(123456), "subscription_group_id": "98765", "monthly_product": "fixture.monthly",
		"annual_product": "fixture.annual", "private_key_pem": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))}
	path := filepath.Join(root, "apple-billing.json")
	write := func() {
		t.Helper()
		raw, err := json.Marshal(fields)
		if err != nil || os.WriteFile(path, raw, 0600) != nil {
			t.Fatal("could not write synthetic credential")
		}
	}
	write()
	t.Setenv("MYCHEF_APPLE_BILLING_SANDBOX_FILE", path)
	mode, err := loadAppleBillingSandbox(cfg, inbox)
	if mode == nil || err != nil || mode.provider.Environment != billing.Sandbox ||
		mode.provider.BundleID != applePreviewBundle || mode.provider.Roots.Equal(x509.NewCertPool()) {
		t.Fatal("private sandbox metadata was not read without contacting the provider", err)
	}
	t.Run("read only worker", func(t *testing.T) {
		_, journal := runtimeDatabase(t)
		child, cancel := context.WithCancel(t.Context())
		defer cancel()
		type request struct {
			method, host, path string
			signed             bool
		}
		hits := make(chan request, 4)
		client := &http.Client{Transport: appleFixtureTransport(func(r *http.Request) (*http.Response, error) {
			select {
			case hits <- request{r.Method, r.URL.Host, r.URL.Path, strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")}:
			default:
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"hasMore":false,"notificationHistory":[]}`))}, nil
		})}
		native, worker, err := mode.start(child, journal, client, nil)
		if err != nil || native == nil || worker == nil || native.CheckoutEnabled || native.PlayCheckoutEnabled || !native.RequireAppleAttestation ||
			native.Play != nil || native.Environment != billing.Sandbox || !native.Available() {
			t.Fatal("sandbox was not read only", err)
		}
		done := make(chan struct{})
		go func() { defer close(done); worker.Run(child) }()
		select {
		case call := <-hits:
			if call.host != "api.storekit-sandbox.apple.com" || call.method != http.MethodPost ||
				call.path != "/inApps/v1/notifications/history" || !call.signed {
				t.Fatal("the worker left the authenticated sandbox history route")
			}
		case <-time.After(2 * time.Second):
			t.Error("offline sandbox worker did not start")
		}
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("sandbox worker did not stop")
		}
	})
	t.Setenv("MYCHEF_APPLE_BILLING_SANDBOX_CHECKOUT", "yes")
	if mode, err := loadAppleBillingSandbox(cfg, inbox); mode != nil || err == nil {
		t.Fatal("ambiguous sandbox checkout setting was accepted", err)
	}
	t.Setenv("MYCHEF_APPLE_BILLING_SANDBOX_CHECKOUT", "1")
	checkoutMode, err := loadAppleBillingSandbox(cfg, inbox)
	if err != nil || checkoutMode == nil {
		t.Fatal("explicit sandbox checkout could not be prepared", err)
	}
	_, checkoutJournal := runtimeDatabase(t)
	native, _, err := checkoutMode.start(t.Context(), checkoutJournal, nil, nil)
	if err != nil || native == nil || !native.CheckoutEnabled || native.PlayCheckoutEnabled || !native.RequireAppleAttestation ||
		native.Environment != billing.Sandbox || !native.SyntheticAccountsOnly {
		t.Fatal("checkout was not bounded to the Apple sandbox and synthetic accounts", err)
	}
	t.Setenv("MYCHEF_APPLE_BILLING_SANDBOX_FILE", "")
	if mode, err := loadAppleBillingSandbox(cfg, inbox); mode != nil || err == nil {
		t.Fatal("checkout setting started without a sandbox configuration", err)
	}
	t.Setenv("MYCHEF_APPLE_BILLING_SANDBOX_FILE", path)
	t.Setenv("MYCHEF_APPLE_BILLING_SANDBOX_CHECKOUT", "")
	public := cfg
	public.Origin = "https://public.example.test"
	if mode, err := loadAppleBillingSandbox(public, inbox); mode != nil || err == nil {
		t.Fatal("public listener could start the sandbox worker", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if mode, err := loadAppleBillingSandbox(cfg, inbox); mode != nil || err == nil {
		t.Fatal("globally readable key was accepted", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	fields["environment"] = "production"
	write()
	if mode, err := loadAppleBillingSandbox(cfg, inbox); mode != nil || err == nil {
		t.Fatal("credential file could change the billing environment", err)
	}
	delete(fields, "environment")
	fields["checkout"] = true
	write()
	if mode, err := loadAppleBillingSandbox(cfg, inbox); mode != nil || err == nil {
		t.Fatal("credential file could enable sales", err)
	}
}

type appleFixtureTransport func(*http.Request) (*http.Response, error)

func (f appleFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
