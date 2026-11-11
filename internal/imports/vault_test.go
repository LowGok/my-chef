package imports

import (
	"bytes"
	"fmt"
	"testing"
)

func TestImportVaultAuthenticatedPrivateDrafts(t *testing.T) {
	key := bytes.Repeat([]byte{42}, 32)
	vault, err := NewDraftVault(key)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("private-import-draft-canary")
	first, err := vault.Seal("operation:owner:expiry", secret)
	if err != nil {
		t.Fatal(err)
	}
	second, err := vault.Seal("operation:owner:expiry", secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) || bytes.Contains(first, secret) {
		t.Fatal("draft not privately randomized")
	}
	decoded, err := vault.Open("operation:owner:expiry", first)
	if err != nil || !bytes.Equal(decoded, secret) {
		t.Fatal("draft did not round trip")
	}
	for _, binding := range []string{"other-operation:owner:expiry", "operation:other-owner:expiry", "operation:owner:other-expiry"} {
		if _, err := vault.Open(binding, first); err == nil {
			t.Fatal("ciphertext accepted another binding")
		}
	}
	first[len(first)-1] ^= 1
	if _, err := vault.Open("operation:owner:expiry", first); err == nil {
		t.Fatal("tampered draft readable")
	}
	if _, err := vault.Seal("binding", bytes.Repeat([]byte("x"), MaxPrivateDraftBytes+1)); err == nil {
		t.Fatal("unbounded draft accepted")
	}
	if _, err := NewDraftVault(make([]byte, 32)); err == nil {
		t.Fatal("missing key enabled drafts")
	}
	if bytes.Contains([]byte(fmt.Sprintf("%#v", vault)), key) {
		t.Fatal("key diagnostic disclosure")
	}
	var missing *DraftVault
	if _, err := missing.Open("binding", first); err == nil {
		t.Fatal("missing vault accepted")
	}
	if vault.Fingerprint("request", []byte("source")) == vault.Fingerprint("source", []byte("source")) {
		t.Fatal("fingerprint purpose not separated")
	}
}

func TestPrivateImportDraftRejectsInvalidStoredPayload(t *testing.T) {
	source, err := CanonicalImportSource(sourceFixture())
	if err != nil {
		t.Fatal(err)
	}
	draft, err := NewDraft(extractionFixture(), source)
	if err != nil {
		t.Fatal(err)
	}
	data, err := (PrivateDraft{Source: source, Draft: &draft}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePrivateDraft(append(bytes.Clone(data), []byte(` {"extra":"private-canary"}`)...)); err == nil {
		t.Fatal("trailing private payload accepted")
	}
	changed := draft
	changed.Content.Title = "unbound draft"
	if _, err := (PrivateDraft{Source: source, Draft: &changed}).Encode(); err == nil {
		t.Fatal("changed draft accepted")
	}
	if _, err := CanonicalImportSource(ImportSource{Kind: "url", URL: source.URL, Title: "bad\x00title", ImportedAt: source.ImportedAt}); err == nil {
		t.Fatal("unsafe source title accepted")
	}
}
