package mailqueue

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

const SignUpProofTemplate = "signup-proof/v1"
const EmailVerificationTemplate = "email-verification/v1"
const EmailChangeCurrentTemplate = "email-change-current/v1"
const EmailChangeProposedTemplate = "email-change-proposed/v1"

var ErrProofUnavailable = errors.New("protected email proof unavailable")
var ErrProofExpired = errors.New("email proof expired")

// ProofKeys is an immutable keyring. The active version encrypts new values;
// readers select the recorded version even after key rotation.
type ProofKeys struct {
	active string
	keys   map[string][32]byte
}

type SealedProof struct {
	Version    string `json:"version"`
	Ciphertext string `json:"ciphertext"`
}

func NewProofKeys(active string, values map[string][]byte) (*ProofKeys, error) {
	if len(values) == 0 || len(values) > 32 {
		return nil, ErrProofUnavailable
	}
	keys := make(map[string][32]byte, len(values))
	for version, raw := range values {
		if version == "" || len(version) > 32 || strings.IndexFunc(version, func(r rune) bool {
			return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_", r)
		}) >= 0 || len(raw) != 32 {
			return nil, ErrProofUnavailable
		}
		keys[version] = [32]byte(raw)
	}
	if _, ok := keys[active]; !ok {
		return nil, ErrProofUnavailable
	}
	return &ProofKeys{active: active, keys: keys}, nil
}

func (k *ProofKeys) aead(version, purpose string) (cipher.AEAD, error) {
	if k == nil || purpose == "" {
		return nil, ErrProofUnavailable
	}
	raw, exists := k.keys[version]
	if !exists {
		return nil, ErrProofUnavailable
	}
	mac := hmac.New(sha256.New, raw[:])
	mac.Write([]byte("mychef-email-proof/v1/" + purpose))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return nil, ErrProofUnavailable
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func associatedData(version, purpose, binding string) []byte {
	value, _ := json.Marshal([3]string{version, purpose, binding})
	return value
}

func (k *ProofKeys) Seal(purpose, binding string, value []byte) (SealedProof, error) {
	if k == nil || len(value) == 0 || len(value) > 16384 {
		return SealedProof{}, ErrProofUnavailable
	}
	aead, err := k.aead(k.active, purpose)
	if err != nil {
		return SealedProof{}, ErrProofUnavailable
	}
	sealed := aead.Seal(nil, nil, value, associatedData(k.active, purpose, binding))
	return SealedProof{Version: k.active, Ciphertext: base64.RawURLEncoding.EncodeToString(sealed)}, nil
}

func (k *ProofKeys) Open(purpose, binding string, box SealedProof) ([]byte, error) {
	if len(box.Ciphertext) > 22000 {
		return nil, ErrProofUnavailable
	}
	aead, err := k.aead(box.Version, purpose)
	if err != nil {
		return nil, ErrProofUnavailable
	}
	sealed, err := base64.RawURLEncoding.DecodeString(box.Ciphertext)
	if err != nil {
		return nil, ErrProofUnavailable
	}
	value, err := aead.Open(nil, nil, sealed, associatedData(box.Version, purpose, binding))
	if err != nil {
		return nil, ErrProofUnavailable
	}
	return value, nil
}

type protectedData struct {
	Digest    string                 `json:"digest"`
	Expires   int64                  `json:"expires"`
	ExpiresNS int64                  `json:"expires_ns,omitempty"`
	Links     map[string]SealedProof `json:"links,omitempty"`
}

func (d protectedData) expiryBinding() int64 {
	if d.ExpiresNS != 0 {
		return d.ExpiresNS
	}
	return d.Expires
}

func ProtectedTemplate(name string) bool {
	return len(proofLinkFields(name)) != 0
}

func proofLinkFields(name string) []string {
	switch name {
	case SignUpProofTemplate, EmailVerificationTemplate, EmailChangeProposedTemplate, RecoveryProofTemplate:
		return []string{"link"}
	case EmailChangeCurrentTemplate:
		return []string{"approve", "cancel"}
	default:
		return nil
	}
}

func validProofFields(name string, values map[string]string, extraFields int) bool {
	fields := proofLinkFields(name)
	if len(fields) == 0 || len(values) != len(fields)+extraFields {
		return false
	}
	for _, field := range fields {
		if !validProofLink(values[field]) {
			return false
		}
		if name == RecoveryProofTemplate {
			link, err := url.Parse(values[field])
			if err != nil || link.Path != "/app/reset-password" {
				return false
			}
		}
	}
	return true
}

func validProofLink(raw string) bool {
	if len(raw) > 2048 {
		return false
	}
	link, err := url.Parse(raw)
	if err != nil || link.Hostname() == "" || link.User != nil || link.RawQuery != "" || link.Opaque != "" {
		return false
	}
	if link.Scheme != "https" {
		ip, err := netip.ParseAddr(link.Hostname())
		if link.Scheme != "http" || err != nil || !ip.IsLoopback() {
			return false
		}
	}
	token, err := base64.RawURLEncoding.Strict().DecodeString(link.Fragment)
	return err == nil && len(token) == 32
}

func linkBinding(row Record, field string, expiry int64) string {
	value, _ := json.Marshal(struct {
		Key, Recipient, Template, Field string
		Expires                         int64
	}{row.Key, row.Recipient, row.Template, field, expiry})
	return string(value)
}

// EnqueueProtected keeps the first ciphertext for a logical message, including
// when the caller retries a transaction after a key rotation.
func EnqueueProtected(ctx context.Context, s *store.Store, tx *gorm.DB, at, expires time.Time, keys *ProofKeys, req Request) (int64, error) {
	if !validRequest(req) || !validProofFields(req.Template, req.Fields, 0) {
		return 0, ErrProofUnavailable
	}
	raw, _ := json.Marshal(req.Fields)
	sum := sha256.Sum256(raw)
	value := protectedData{Digest: hex.EncodeToString(sum[:]), Expires: expires.Unix()}
	if req.Template == RecoveryProofTemplate {
		value.ExpiresNS = expires.UnixNano()
	}
	row := Record{Key: req.Key, Recipient: req.Recipient, Template: req.Template, State: "queued",
		NextAt: at.Unix(), CreatedAt: at.Unix(), UpdatedAt: at.Unix()}
	save := func(db *gorm.DB) error {
		var old Record
		err := db.Where("key = ?", req.Key).Take(&old).Error
		if err == nil {
			var saved protectedData
			if old.Template != row.Template || old.Recipient != row.Recipient ||
				json.Unmarshal([]byte(old.Data), &saved) != nil || saved.Digest != value.Digest || saved.Expires != value.Expires || saved.ExpiresNS != value.ExpiresNS {
				return ErrKeyConflict
			}
			row.ID = old.ID
			return store.LinkAccountMail(db, req.OwnerID, row.ID)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrStorage
		}
		if (value.ExpiresNS != 0 && !at.Before(expires)) || (value.ExpiresNS == 0 && value.Expires <= at.Unix()) {
			return ErrProofExpired
		}
		value.Links = make(map[string]SealedProof, len(req.Fields))
		for _, field := range proofLinkFields(req.Template) {
			box, err := keys.Seal("outbox-link", linkBinding(row, field, value.expiryBinding()), []byte(req.Fields[field]))
			if err != nil {
				return err
			}
			value.Links[field] = box
		}
		data, _ := json.Marshal(value)
		row.Data = string(data)
		if err = db.Create(&row).Error; err != nil {
			return ErrStorage
		}
		return store.LinkAccountMail(db, req.OwnerID, row.ID)
	}
	var err error
	if tx == nil {
		err = s.DB.WithContext(ctx).Transaction(save)
	} else {
		err = save(tx.WithContext(ctx))
	}
	if err != nil {
		return 0, err
	}
	return row.ID, nil
}

// OpenProtected is called by the delivery worker before rendering. It does not
// mutate the persisted encrypted message.
func OpenProtected(keys *ProofKeys, row Record, now time.Time, allowLoopback bool) (map[string]string, int64, error) {
	var data protectedData
	names := proofLinkFields(row.Template)
	if len(names) == 0 || json.Unmarshal([]byte(row.Data), &data) != nil || len(data.Links) != len(names) {
		return nil, 0, ErrProofUnavailable
	}
	expires := time.Unix(data.Expires, 0)
	retryUntil := data.Expires
	if row.Template == RecoveryProofTemplate {
		if data.ExpiresNS <= 0 || time.Unix(0, data.ExpiresNS).Unix() != data.Expires {
			return nil, 0, ErrProofUnavailable
		}
		expires = time.Unix(0, data.ExpiresNS)
		if expires.Nanosecond() != 0 {
			retryUntil++
		}
	} else if data.ExpiresNS != 0 {
		return nil, 0, ErrProofUnavailable
	}
	if !now.Before(expires) {
		return nil, 0, ErrProofExpired
	}
	fields := map[string]string{"expires": expires.UTC().Format(time.RFC3339)}
	for _, name := range names {
		raw, err := keys.Open("outbox-link", linkBinding(row, name, data.expiryBinding()), data.Links[name])
		if err != nil || !validProofLink(string(raw)) || (!allowLoopback && !strings.HasPrefix(string(raw), "https://")) {
			return nil, 0, ErrProofUnavailable
		}
		fields[name] = string(raw)
	}
	return fields, retryUntil, nil
}

// ErasedProtectedData preserves the enqueue identity but not recoverable data.
func ErasedProtectedData(row Record) string {
	var data protectedData
	if json.Unmarshal([]byte(row.Data), &data) != nil {
		return "{}"
	}
	data.Links = nil
	encoded, _ := json.Marshal(data)
	return string(encoded)
}

// InvalidateProtected joins the transaction that invalidates the proof. An
// in-flight provider response cannot move the row out of its terminal state.
func InvalidateProtected(ctx context.Context, tx *gorm.DB, now time.Time, key string) error {
	return eraseProtected(ctx, tx, now, key, "proof_invalidated")
}

func eraseProtected(ctx context.Context, tx *gorm.DB, now time.Time, key, reason string) error {
	var row Record
	db := tx.WithContext(ctx)
	err := db.Where("key = ? AND state IN ?", key, []string{"queued", "sending"}).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil || !ProtectedTemplate(row.Template) {
		return ErrStorage
	}
	if err = db.Model(&row).Updates(map[string]any{"state": "failed", "reason": reason,
		"data": ErasedProtectedData(row), "updated_at": now.Unix()}).Error; err != nil {
		return ErrStorage
	}
	return nil
}

// RecoveryMailKey identifies one issued proof without exposing the secret.
func RecoveryMailKey(order int64) string { return "password-recovery/" + strconv.FormatInt(order, 10) }

// SweepRecoverySlice erases one bounded batch independently of the sender.
// The caller schedules it separately and owns the surrounding transaction.
func SweepRecoverySlice(ctx context.Context, tx *gorm.DB, now time.Time) (bool, error) {
	if tx == nil {
		return false, ErrStorage
	}
	const batch = 16
	db := tx.WithContext(ctx)
	removed := db.Exec(`DELETE FROM recovery_jobs WHERE accepted_order IN (
SELECT accepted_order FROM recovery_jobs WHERE expires_ns <= ? ORDER BY expires_ns, accepted_order LIMIT ?)`, now.UnixNano(), batch)
	if removed.Error != nil {
		return false, ErrStorage
	}
	var expired []store.RecoveryProof
	if err := db.Where("expires_ns <= ?", now.UnixNano()).Order("expires_ns, accepted_order").Limit(batch).Find(&expired).Error; err != nil {
		return false, ErrStorage
	}
	for _, proof := range expired {
		if err := eraseProtected(ctx, tx, now, RecoveryMailKey(proof.AcceptedOrder), "proof_expired"); err != nil {
			return false, err
		}
		if err := db.Delete(&proof).Error; err != nil {
			return false, ErrStorage
		}
	}
	return removed.RowsAffected == batch || len(expired) == batch, nil
}
