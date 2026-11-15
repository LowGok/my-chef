package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/LowGok/my-chef/internal/billing"
	"github.com/LowGok/my-chef/internal/config"
	httpapi "github.com/LowGok/my-chef/internal/http"
	"github.com/LowGok/my-chef/internal/store"
)

const applePreviewBundle = "com.gkldevelopment.mychef.preview"
const appleRootG3SHA256 = "63343abfb89a6a03ebb57e9b3f5fa7be7c4f5c756f3017b3a8c488c3653e9179"

var (
	errAppleSandbox     = errors.New("invalid private Apple billing sandbox configuration")
	applePreviewProduct = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	applePreviewGroup   = regexp.MustCompile(`^[0-9]{1,32}$`)
	applePreviewAppID   = regexp.MustCompile(`^[A-Z0-9]{10}\.` + regexp.QuoteMeta(applePreviewBundle) + `$`)
	applePreviewBuild   = regexp.MustCompile(`^[0-9]{1,24}(\.[0-9]{1,12}){0,2}$`)
)

type appleBillingSandbox struct {
	provider    billing.AppleConfig
	expires     time.Time
	checkout    bool
	attestation *billing.AppleEnrollmentVerifier
}

type appleBillingCredential struct {
	Issuer  string `json:"issuer_id"`
	KeyID   string `json:"key_id"`
	AppleID int64  `json:"app_apple_id"`
	Group   string `json:"subscription_group_id"`
	Monthly string `json:"monthly_product"`
	Annual  string `json:"annual_product"`
	KeyPEM  string `json:"private_key_pem"`
	Attest  *struct {
		AppID   string `json:"app_id"`
		Version string `json:"bundle_version"`
	} `json:"app_attest,omitempty"`
}

func loadAppleBillingSandbox(app config.Config, inbox *config.FixtureInbox) (*appleBillingSandbox, error) {
	path := os.Getenv("MYCHEF_APPLE_BILLING_SANDBOX_FILE")
	var checkout bool
	switch os.Getenv("MYCHEF_APPLE_BILLING_SANDBOX_CHECKOUT") {
	case "", "0":
	case "1":
		checkout = true
	default:
		return nil, errAppleSandbox
	}
	if path == "" {
		if checkout {
			return nil, errAppleSandbox
		}
		return nil, nil
	}
	if !privateBillingSandbox(app, inbox, time.Now()) {
		return nil, errAppleSandbox
	}
	raw, err := readPrivateBillingSandboxCredential(path)
	if err != nil {
		return nil, errAppleSandbox
	}
	defer clear(raw)
	var saved appleBillingCredential
	if err := json.Unmarshal(raw, &saved, json.RejectUnknownMembers(true)); err != nil ||
		saved.AppleID < 1 || !applePreviewGroup.MatchString(saved.Group) ||
		!applePreviewProduct.MatchString(saved.Monthly) || !applePreviewProduct.MatchString(saved.Annual) ||
		saved.Monthly == saved.Annual {
		return nil, errAppleSandbox
	}
	pemKey := []byte(saved.KeyPEM)
	defer clear(pemKey)
	block, trailing := pem.Decode(pemKey)
	if block == nil || block.Type != "PRIVATE KEY" || len(block.Headers) != 0 || len(bytes.TrimSpace(trailing)) != 0 {
		return nil, errAppleSandbox
	}
	defer clear(block.Bytes)
	private, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	key, ok := private.(*ecdsa.PrivateKey)
	if err != nil || !ok || key.Curve != elliptic.P256() {
		return nil, errAppleSandbox
	}
	roots, err := appleSandboxRoots()
	if err != nil {
		return nil, err
	}
	var attestation *billing.AppleEnrollmentVerifier
	if checkout {
		if saved.Attest == nil || !applePreviewAppID.MatchString(saved.Attest.AppID) || !applePreviewBuild.MatchString(saved.Attest.Version) {
			return nil, errAppleSandbox
		}
		// TestFlight App Attest uses its production root/AAGUID regardless of
		// StoreKit's independent purchase sandbox. Require signed build metadata.
		build := billing.AppleAttestBuildPolicy{Category: 2, Version: saved.Attest.Version}
		configured, err := billing.NewAppleEnrollmentVerifier(saved.Attest.AppID, build, false)
		if err != nil {
			return nil, errAppleSandbox
		}
		attestation = &configured
	}
	mode := &appleBillingSandbox{expires: inbox.Expires, checkout: checkout, attestation: attestation, provider: billing.AppleConfig{
		BundleID: applePreviewBundle, GroupID: saved.Group, Issuer: saved.Issuer, KeyID: saved.KeyID,
		AppAppleID: saved.AppleID, Environment: billing.Sandbox, Key: key, Roots: roots,
		Products: map[string]billing.Interval{saved.Monthly: billing.Monthly, saved.Annual: billing.Annual},
	}}
	if _, err := billing.NewAppleProvider(mode.provider, nil, nil); err != nil {
		return nil, errAppleSandbox
	}
	return mode, nil
}

func appleSandboxRoots() (*x509.CertPool, error) {
	der, err := base64.StdEncoding.Strict().DecodeString(appleRootG3DER)
	hash := sha256.Sum256(der)
	if err != nil || hex.EncodeToString(hash[:]) != appleRootG3SHA256 {
		return nil, errAppleSandbox
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil || !cert.IsCA || cert.CheckSignatureFrom(cert) != nil {
		return nil, errAppleSandbox
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool, nil
}

func (mode *appleBillingSandbox) start(ctx context.Context, journal *store.LifecycleJournal, client *http.Client,
	report func(billing.AppleWorkerHealth)) (*httpapi.NativeBilling, *billing.AppleWorker, error) {
	allowed := func() bool { return time.Now().Before(mode.expires) }
	if !allowed() || journal == nil || mode.provider.Environment != billing.Sandbox || mode.provider.BundleID != applePreviewBundle || (mode.checkout && mode.attestation == nil) {
		return nil, nil, errAppleSandbox
	}
	provider, err := billing.NewAppleProvider(mode.provider, client, nil)
	if err != nil {
		return nil, nil, errAppleSandbox
	}
	protected, err := journal.Billing(ctx)
	if err != nil {
		return nil, nil, err
	}
	offers := make([]billing.Offer, 0, len(mode.provider.Products))
	for id, interval := range mode.provider.Products {
		offers = append(offers, billing.Offer{Store: billing.Apple, Environment: billing.Sandbox,
			App: applePreviewBundle, Product: id, Interval: interval})
	}
	worker, err := billing.NewAppleWorker(provider, protected, offers, journal.WithLiveSandboxBillingOwner, report)
	if err != nil {
		return nil, nil, err
	}
	var attestation *httpapi.AppleBillingAttestation
	if mode.checkout {
		keys := billing.AppleCancellationRegistry{
			Journal: protected, Environment: billing.Sandbox, Verifier: *mode.attestation,
		}
		attestation = &httpapi.AppleBillingAttestation{
			Registry: keys,
			Verifier: billing.AppleCancellationVerifier{
				AppID: mode.attestation.AppID, Keys: keys, Build: mode.attestation.Build,
			},
		}
	}
	return &httpapi.NativeBilling{Journal: protected, Environment: billing.Sandbox, Offers: offers,
		Available: allowed, CheckoutEnabled: mode.checkout, SyntheticAccountsOnly: true, RequireAppleAttestation: true,
		Providers: map[billing.Store]billing.Provider{billing.Apple: provider}, AppleAttestation: attestation}, worker, nil
}

// Apple Root CA G3, published at https://www.apple.com/certificateauthority/.
const appleRootG3DER = "MIICQzCCAcmgAwIBAgIILcX8iNLFS5UwCgYIKoZIzj0EAwMwZzEbMBkGA1UEAwwSQXBwbGUgUm9v" +
	"dCBDQSAtIEczMSYwJAYDVQQLDB1BcHBsZSBDZXJ0aWZpY2F0aW9uIEF1dGhvcml0eTETMBEGA1UE" +
	"CgwKQXBwbGUgSW5jLjELMAkGA1UEBhMCVVMwHhcNMTQwNDMwMTgxOTA2WhcNMzkwNDMwMTgxOTA2" +
	"WjBnMRswGQYDVQQDDBJBcHBsZSBSb290IENBIC0gRzMxJjAkBgNVBAsMHUFwcGxlIENlcnRpZmlj" +
	"YXRpb24gQXV0aG9yaXR5MRMwEQYDVQQKDApBcHBsZSBJbmMuMQswCQYDVQQGEwJVUzB2MBAGByqG" +
	"SM49AgEGBSuBBAAiA2IABJjpLz1AcqTtkyJygRMc3RCV8cWjTnHcFBbZDuWmBSp3ZHtfTjjTuxxE" +
	"tX/1H7YyYl3J6YRbTzBPEVoA/VhYDKX1DyxNB0cTddqXl5dvMVztK517IDvYuVTZXpmkOlEKMaNC" +
	"MEAwHQYDVR0OBBYEFLuw3qFYM4iapIqZ3r6966/ayySrMA8GA1UdEwEB/wQFMAMBAf8wDgYDVR0P" +
	"AQH/BAQDAgEGMAoGCCqGSM49BAMDA2gAMGUCMQCD6cHEFl4aXTQY2e3v9GwOAEZLuN+yRhHFD/3m" +
	"eoyhpmvOwgPUnPWTxnS4at+qIxUCMG1mihDK1A3UT82NQz60imOlM27jbdoXt2QfyFMm+YhidDkL" +
	"F1vLUagM6BgD56KyKA=="
