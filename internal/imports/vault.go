package imports

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

const MaxPrivateDraftBytes = 2 << 20

var ErrPrivateDraft = errors.New("private recipe draft unavailable")

// DraftVault keys live outside the application database and its snapshots.
// Replacing the key does not silently re-create or decrypt an older draft.
type DraftVault struct {
	aead           cipher.AEAD
	fingerprintKey []byte
}

func (*DraftVault) String() string   { return "private recipe-draft vault" }
func (*DraftVault) GoString() string { return "private recipe-draft vault" }
func NewDraftVault(key []byte) (*DraftVault, error) {
	if len(key) != 32 {
		return nil, ErrPrivateDraft
	}
	nonzero := byte(0)
	for _, b := range key {
		nonzero |= b
	}
	if nonzero == 0 {
		return nil, ErrPrivateDraft
	}
	derive := func(purpose string) []byte {
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write([]byte("mychef-import-v1:" + purpose))
		return mac.Sum(nil)
	}
	encryption := derive("encryption")
	defer clear(encryption)
	block, e := aes.NewCipher(encryption)
	if e != nil {
		return nil, ErrPrivateDraft
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		return nil, ErrPrivateDraft
	}
	return &DraftVault{aead: aead, fingerprintKey: derive("fingerprint")}, nil
}
func (v *DraftVault) Seal(binding string, plain []byte) ([]byte, error) {
	if v == nil || v.aead == nil || len(binding) == 0 || len(binding) > 512 || len(plain) == 0 || len(plain) > MaxPrivateDraftBytes {
		return nil, ErrPrivateDraft
	}
	output := make([]byte, 1+v.aead.NonceSize())
	output[0] = 1
	if _, e := rand.Read(output[1:]); e != nil {
		return nil, ErrPrivateDraft
	}
	return v.aead.Seal(output, output[1:], plain, []byte("mychef-import-draft-v1:"+binding)), nil
}
func (v *DraftVault) Open(binding string, sealed []byte) ([]byte, error) {
	if v == nil || v.aead == nil || len(binding) == 0 || len(binding) > 512 || len(sealed) > MaxPrivateDraftBytes+64 || len(sealed) < 1+v.aead.NonceSize()+v.aead.Overhead() || sealed[0] != 1 {
		return nil, ErrPrivateDraft
	}
	offset := 1 + v.aead.NonceSize()
	plain, e := v.aead.Open(nil, sealed[1:offset], sealed[offset:], []byte("mychef-import-draft-v1:"+binding))
	if e != nil {
		return nil, ErrPrivateDraft
	}
	return plain, nil
}
func (v *DraftVault) Fingerprint(purpose string, value []byte) string {
	if v == nil || v.aead == nil || len(purpose) == 0 || len(purpose) > 128 || len(value) > MaxPrivateDraftBytes {
		return ""
	}
	mac := hmac.New(sha256.New, v.fingerprintKey)
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(value)
	return hex.EncodeToString(mac.Sum(nil))
}
