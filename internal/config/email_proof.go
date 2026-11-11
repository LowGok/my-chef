package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
)

type EmailProofKeys struct {
	Active  string
	Keys    map[string][]byte
	RateKey []byte
}

// LoadEmailProofKeys reads the keyring from a file that lives outside SQLite.
// An unset path leaves proof delivery unavailable.
func LoadEmailProofKeys() (EmailProofKeys, error) {
	path := os.Getenv("MYCHEF_EMAIL_PROOF_KEYS_FILE")
	if path == "" {
		return EmailProofKeys{}, nil
	}
	invalid := errors.New("invalid email proof key file")
	file, err := os.Open(path)
	if err != nil {
		return EmailProofKeys{}, invalid
	}
	defer func() { _ = file.Close() }()
	const maximum = 8 * 1024
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || len(data) > maximum {
		return EmailProofKeys{}, invalid
	}
	var document struct {
		Active  string            `json:"active"`
		Keys    map[string]string `json:"keys"`
		RateKey string            `json:"rate_key"`
	}
	reader := json.NewDecoder(bytes.NewReader(data))
	reader.DisallowUnknownFields()
	if err := reader.Decode(&document); err != nil || reader.Decode(new(any)) != io.EOF ||
		len(document.Keys) == 0 || len(document.Keys) > 32 {
		return EmailProofKeys{}, invalid
	}
	rateKey, err := base64.StdEncoding.Strict().DecodeString(document.RateKey)
	if err != nil || len(rateKey) != 32 || base64.StdEncoding.EncodeToString(rateKey) != document.RateKey {
		return EmailProofKeys{}, invalid
	}
	keys := make(map[string][]byte, len(document.Keys))
	for version, encoded := range document.Keys {
		key, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil || len(key) != 32 || base64.StdEncoding.EncodeToString(key) != encoded || bytes.Equal(key, rateKey) {
			return EmailProofKeys{}, invalid
		}
		keys[version] = key
	}
	if document.Active == "" || keys[document.Active] == nil {
		return EmailProofKeys{}, invalid
	}
	return EmailProofKeys{Active: document.Active, Keys: keys, RateKey: rateKey}, nil
}
