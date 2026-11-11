package config

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestEmailProofKeysFile(t *testing.T) {
	key := bytes.Repeat([]byte{3}, 32)
	encoded := base64.StdEncoding.EncodeToString(key)
	rateKey := bytes.Repeat([]byte{5}, 32)
	rateEncoded := base64.StdEncoding.EncodeToString(rateKey)
	path := filepath.Join(t.TempDir(), "fixture.json")
	t.Setenv("MYCHEF_EMAIL_PROOF_KEYS_FILE", path)
	for _, tc := range []struct {
		name, data string
		valid      bool
	}{
		{"active key", `{"active":"a","keys":{"a":"` + encoded + `"},"rate_key":"` + rateEncoded + `"}`, true},
		{"missing rate key", `{"active":"a","keys":{"a":"` + encoded + `"}}`, false},
		{"shared key material", `{"active":"a","keys":{"a":"` + encoded + `"},"rate_key":"` + encoded + `"}`, false},
		{"missing active", `{"active":"b","keys":{"a":"` + encoded + `"}}`, false},
		{"invalid material", `{"active":"a","keys":{"a":"short"}}`, false},
		{"trailing data", `{"active":"a","keys":{"a":"` + encoded + `"}} {}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			settings, err := LoadEmailProofKeys()
			if tc.valid {
				if err != nil || settings.Active != "a" || !bytes.Equal(settings.Keys["a"], key) || !bytes.Equal(settings.RateKey, rateKey) {
					t.Fatalf("valid keyring rejected: %v", err)
				}
			} else if err == nil || err.Error() != "invalid email proof key file" {
				t.Fatal("invalid keyring must return a generic error")
			}
		})
	}
}
