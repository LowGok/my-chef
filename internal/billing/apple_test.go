package billing

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

type appleTransport func(*http.Request) (*http.Response, error)

func (fn appleTransport) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func appleSignFixture(t *testing.T, h, body any, key *ecdsa.PrivateKey) string {
	t.Helper()
	part := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	msg := part(h) + "." + part(body)
	hash := sha256.Sum256([]byte(msg))
	r, s, err := ecdsa.Sign(rand.Reader, key, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	r.FillBytes(buf[:32])
	s.FillBytes(buf[32:])
	return msg + "." + base64.RawURLEncoding.EncodeToString(buf)
}

func appleChainFixture(t *testing.T, now time.Time, withOID bool) (*x509.CertPool, []string, *ecdsa.PrivateKey) {
	t.Helper()
	newKey := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	makeCert := func(serial int64, ca bool, oid asn1.ObjectIdentifier, parent *x509.Certificate, issuer *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
		key := newKey()
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "Test certificate"},
			NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true, IsCA: ca, KeyUsage: x509.KeyUsageDigitalSignature}
		if ca {
			template.KeyUsage |= x509.KeyUsageCertSign
		}
		if oid != nil {
			template.ExtraExtensions = []pkix.Extension{{Id: oid, Value: []byte{5, 0}}}
		}
		if parent == nil {
			parent, issuer = template, key
		}
		der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, issuer)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return cert, key
	}
	root, rootKey := makeCert(1, true, nil, nil, nil)
	mid, midKey := makeCert(2, true, asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 2, 1}, root, rootKey)
	var oid asn1.ObjectIdentifier
	if withOID {
		oid = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 11, 1}
	}
	leaf, leafKey := makeCert(3, false, oid, mid, midKey)
	chain := []string{}
	for _, cert := range []*x509.Certificate{leaf, mid, root} {
		chain = append(chain, base64.StdEncoding.EncodeToString(cert.Raw))
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	return roots, chain, leafKey
}

func TestAppleVerifiesAuthoritativeSignedPaidTransaction(t *testing.T) {
	now := time.Date(2026, 11, 14, 18, 0, 0, 0, time.UTC)
	roots, chain, leaf := appleChainFixture(t, now, true)
	_, _, wrongKey := appleChainFixture(t, now, false)
	cfg := AppleConfig{BundleID: "com.example.preview", GroupID: "group-1", Environment: Sandbox,
		Issuer: "00000000-0000-0000-0000-000000000001", KeyID: "ABCDEFGHIJ", Key: wrongKey, Roots: roots,
		Products: map[string]Interval{"premium-monthly": Monthly, "premium-yearly": Annual}}
	body := map[string]any{"bundleId": cfg.BundleID, "subscriptionGroupIdentifier": cfg.GroupID, "productId": "premium-monthly",
		"transactionId": "123456", "originalTransactionId": "123455", "appAccountToken": "12345678-90ab-4cde-8123-4567890abcde",
		"environment": "Sandbox", "storefront": "BEL", "type": "Auto-Renewable Subscription", "inAppOwnershipType": "PURCHASED",
		"purchaseDate": now.Add(-time.Minute).UnixMilli(), "expiresDate": now.Add(time.Hour).UnixMilli(), "signedDate": now.UnixMilli(),
		"currency": "EUR", "price": 6990}
	header := map[string]any{"alg": "ES256", "x5c": chain}
	var reply string
	var calls int
	client := &http.Client{Transport: appleTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "GET" || req.URL.String() != "https://api.storekit-sandbox.apple.com/inApps/v1/transactions/123456" {
			t.Fatal("transaction lookup was not bound to the sandbox host and requested identifier")
		}
		jwt := strings.Split(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "), ".")
		if len(jwt) != 3 {
			t.Fatal("missing Apple authentication")
		}
		payload, err := base64.RawURLEncoding.DecodeString(jwt[1])
		var claims struct {
			Audience string `json:"aud"`
			Bundle   string `json:"bid"`
			Iss      string `json:"iss"`
			Iat, Exp int64
		}
		if err != nil || json.Unmarshal(payload, &claims) != nil || claims.Bundle != cfg.BundleID || claims.Iss != cfg.Issuer ||
			claims.Audience != "appstoreconnect-v1" || claims.Iat != now.Unix() || claims.Exp <= claims.Iat || claims.Exp-claims.Iat > 300 {
			t.Fatal("Apple bearer did not have narrowly scoped claims")
		}
		sig, err := base64.RawURLEncoding.DecodeString(jwt[2])
		sum := sha256.Sum256([]byte(jwt[0] + "." + jwt[1]))
		if err != nil || len(sig) != 64 || !ecdsa.Verify(&cfg.Key.PublicKey, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			t.Fatal("Apple bearer was not signed with configured key")
		}
		b, _ := json.Marshal(map[string]string{"signedTransactionInfo": reply})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	})}
	p, err := NewAppleProvider(cfg, client, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	reply = appleSignFixture(t, header, body, leaf)
	got, err := p.Verify(context.Background(), "123456")
	if err != nil || got.Period.Store != Apple || got.Period.Environment != Sandbox || got.Period.Transaction != "123456" ||
		got.Period.Subscription != "123455" || !got.Period.PaidThrough.Equal(now.Add(time.Hour)) || got.Trial {
		t.Fatalf("valid signed transaction not normalized: %+v %v", got, err)
	}
	for _, x := range []struct {
		key   string
		value any
	}{
		{"environment", "Production"}, {"bundleId", "com.other.preview"}, {"transactionId", "789"}, {"productId", "unknown"},
		{"subscriptionGroupIdentifier", "other-group"}, {"inAppOwnershipType", "FAMILY_SHARED"}, {"price", 0}, {"price", 7990},
		{"offerType", 1}, {"signedDate", now.Add(time.Hour).UnixMilli()}, {"billingPlanType", "MONTHLY"},
		{"commitmentInfo", map[string]any{"commitmentExpiresDate": now.AddDate(1, 0, 0).UnixMilli()}},
	} {
		old, ok := body[x.key]
		body[x.key] = x.value
		reply = appleSignFixture(t, header, body, leaf)
		if _, err := p.Verify(context.Background(), "123456"); !errors.Is(err, ErrPurchaseBinding) {
			t.Errorf("%s mismatch accepted: %v", x.key, err)
		}
		if ok {
			body[x.key] = old
		} else {
			delete(body, x.key)
		}
	}
	body["billingPlanType"] = "BILLED_UPFRONT"
	body["productId"] = "premium-yearly"
	body["price"] = 69900
	body["revocationDate"] = now.UnixMilli()
	reply = appleSignFixture(t, header, body, leaf)
	got, err = p.Verify(context.Background(), "123456")
	if err != nil || got.Period.Interval != Annual || !got.Period.Revoked {
		t.Fatalf("standard annual transaction did not retain Apple's revocation: %+v %v", got, err)
	}
	_, counterfeitChain, counterfeitLeaf := appleChainFixture(t, now, true)
	reply = appleSignFixture(t, map[string]any{"alg": "ES256", "x5c": counterfeitChain}, body, counterfeitLeaf)
	if _, err := p.Verify(context.Background(), "123456"); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("response-supplied root was trusted", err)
	}
	reply = appleSignFixture(t, header, body, wrongKey)
	if _, err := p.Verify(context.Background(), "123456"); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("bad signature accepted", err)
	}
	before := calls
	if _, err := p.Verify(context.Background(), "../subscriptions"); !errors.Is(err, ErrPurchaseBinding) || before != calls {
		t.Fatal("invalid transaction identifier reached Apple")
	}
	delete(body, "revocationDate")
	body["storefront"], body["currency"], body["price"] = "USA", "USD", 74900
	reply = appleSignFixture(t, header, body, leaf)
	if _, err := p.Verify(context.Background(), "123456"); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("different terms were accepted for a new Belgian purchase", err)
	}
	if proof, err := p.VerifyExisting(context.Background(), "123456"); err != nil || proof.Region != "USA" || proof.Period.Revoked {
		t.Fatal("existing period was lost with another storefront's price", err)
	}
	delete(body, "currency")
	delete(body, "price")
	body["revocationDate"] = now.UnixMilli()
	reply = appleSignFixture(t, header, body, leaf)
	if proof, err := p.VerifyExisting(context.Background(), "123456"); err != nil || !proof.Period.Revoked {
		t.Fatal("terminal fact was lost without price", err)
	}
}
