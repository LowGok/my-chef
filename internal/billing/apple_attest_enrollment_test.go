package billing

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

func TestAppleAttestationEnrollmentAuthenticatesAppAndBuild(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	newKey := func() *ecdsa.PrivateKey {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	cert := func(serial int64, parent *x509.Certificate, pub *ecdsa.PublicKey, issuer *ecdsa.PrivateKey,
		isCA bool, extensions []pkix.Extension) []byte {
		t.Helper()
		tmpl := x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "Synthetic attestation"},
			NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true,
			IsCA: isCA, KeyUsage: x509.KeyUsageDigitalSignature, ExtraExtensions: extensions}
		if isCA {
			tmpl.KeyUsage = x509.KeyUsageCertSign
		}
		if parent == nil {
			parent = &tmpl
		}
		data, err := x509.CreateCertificate(rand.Reader, &tmpl, parent, pub, issuer)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	readCert := func(data []byte) *x509.Certificate {
		t.Helper()
		got, err := x509.ParseCertificate(data)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	rootKey, issuerKey, appKey := newKey(), newKey(), newKey()
	root := readCert(cert(1, nil, &rootKey.PublicKey, rootKey, true, nil))
	issuerDER := cert(2, root, &issuerKey.PublicKey, rootKey, true, nil)
	issuer := readCert(issuerDER)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	point, err := appKey.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	keyID := sha256.Sum256(point.Bytes())
	challenge := [32]byte{15, 98}
	encode := func(obj any) []byte {
		t.Helper()
		b, err := cbor.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	wire := func(app, build, environment string, category uint32, count uint32) []byte {
		t.Helper()
		var auth [87]byte
		appHash := sha256.Sum256([]byte(app))
		copy(auth[:32], appHash[:])
		auth[32] = 0xc0 // attested credential plus signed extension data
		binary.BigEndian.PutUint32(auth[33:37], count)
		copy(auth[37:53], environment)
		binary.BigEndian.PutUint16(auth[53:55], 32)
		copy(auth[55:], keyID[:])
		x, y := point.Bytes()[1:33], point.Bytes()[33:]
		payload := append(auth[:], encode(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})...)
		payload = append(payload, encode(map[string]any{"apple_validation_category_01": category, "apple_bundle_version_01": build})...)
		client := sha256.Sum256(challenge[:])
		digest := sha256.Sum256(append(append([]byte(nil), payload...), client[:]...))
		extension, err := asn1.Marshal(struct {
			Nonce []byte `asn1:"tag:1,explicit"`
		}{Nonce: digest[:]})
		if err != nil {
			t.Fatal(err)
		}
		leaf := cert(3, issuer, &appKey.PublicKey, issuerKey, false, []pkix.Extension{
			{Id: asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 8, 2}, Value: extension},
		})
		return encode(map[string]any{"fmt": "apple-appattest", "authData": payload,
			"attStmt": map[string]any{"x5c": [][]byte{leaf, issuerDER}, "receipt": []byte("synthetic non-authority receipt")}})
	}
	v := AppleEnrollmentVerifier{AppID: fixtureAppleApp, Roots: roots, Build: AppleAttestBuildPolicy{Category: 2, Version: "8"}}
	payload := wire(fixtureAppleApp, "8", "appattest", 2, 0)
	if k, err := v.Verify(payload, challenge, keyID, now); err != nil || !k.Equal(&appKey.PublicKey) {
		t.Fatal("the valid app key was not authenticated", err)
	}
	fails := []struct {
		name string
		data []byte
	}{
		{"wrong app", wire("TEAM000000.example.other", "8", "appattest", 2, 0)},
		{"wrong version", wire(fixtureAppleApp, "7", "appattest", 2, 0)},
		{"wrong category", wire(fixtureAppleApp, "8", "appattest", 4, 0)},
		{"wrong environment", wire(fixtureAppleApp, "8", "appattestdevelop", 2, 0)},
		{"noninitial counter", wire(fixtureAppleApp, "8", "appattest", 2, 3)},
		{"trailing CBOR", append(append([]byte(nil), payload...), 0)},
	}
	for _, tc := range fails {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := v.Verify(tc.data, challenge, keyID, now); !errors.Is(err, ErrPurchaseBinding) {
				t.Fatal("untrusted enrollment accepted", err)
			}
		})
	}
	altered := challenge
	altered[0] ^= 1
	for _, tc := range []struct {
		verifier AppleEnrollmentVerifier
		input    [32]byte
		key      [32]byte
		now      time.Time
	}{
		{v, altered, keyID, now},
		{v, challenge, [32]byte{123}, now},
		{v, challenge, keyID, now.Add(2 * time.Hour)},
		{AppleEnrollmentVerifier{AppID: v.AppID, Roots: x509.NewCertPool(), Build: v.Build}, challenge, keyID, now},
	} {
		if _, err := tc.verifier.Verify(payload, tc.input, tc.key, tc.now); !errors.Is(err, ErrPurchaseBinding) {
			t.Fatal("untrusted key, challenge or certificate accepted", err)
		}
	}
}
