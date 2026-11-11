package mailqueue

import (
	"bytes"
	"testing"
)

func TestProofCiphertextBindsPurposeAndRecord(t *testing.T) {
	key := bytes.Repeat([]byte{3}, 32)
	ring, err := NewProofKeys("old", map[string][]byte{"old": key})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("fixture link or draft")
	first, err := ring.Seal("signup-draft", "proof/1", body)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ring.Seal("signup-draft", "proof/1", body)
	if err != nil || first.Ciphertext == second.Ciphertext {
		t.Fatal("encryption must use a fresh nonce")
	}
	next, err := NewProofKeys("new", map[string][]byte{"old": key, "new": bytes.Repeat([]byte{4}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := next.Open("signup-draft", "proof/1", first); err != nil || !bytes.Equal(decoded, body) {
		t.Fatal("retained key could not open the original draft")
	}
	if _, err := next.Open("outbox-link", "proof/1", first); err == nil {
		t.Fatal("ciphertext accepted for a different purpose")
	}
	if _, err := next.Open("signup-draft", "proof/2", first); err == nil {
		t.Fatal("ciphertext accepted for a different proof")
	}
	first.Version = "new"
	if _, err := next.Open("signup-draft", "proof/1", first); err == nil {
		t.Fatal("ciphertext accepted with a substituted key version")
	}
}
