package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	jsonv2 "encoding/json/v2"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

// PersonalFixture is wired only after LoadFixtureInbox validates the isolated
// DEV/E2E loopback runtime, fake email and the fixture recipe provider.
type PersonalFixture struct {
	store    *store.Store
	settings config.FixtureInbox
	now      func() time.Time
	mu       sync.Mutex
	grants   map[string]store.PersonalEntitlement
}

func NewPersonalFixture(db *store.Store, settings config.FixtureInbox, now func() time.Time) *PersonalFixture {
	if now == nil {
		now = time.Now
	}
	return &PersonalFixture{store: db, settings: settings, now: now, grants: map[string]store.PersonalEntitlement{}}
}

func personalSyntheticAccount(tx *gorm.DB, user int64, incarnation string) bool {
	var email string
	err := tx.Raw(`SELECT u.email FROM users u JOIN account_incarnations a ON a.user_id=u.user_id
WHERE u.user_id=? AND a.incarnation=? AND u.email_verified_at IS NOT NULL AND u.email_verified_at>0`, user, incarnation).Row().Scan(&email)
	if err != nil {
		return false
	}
	display, _, err := store.EmailIdentity(email)
	return err == nil && strings.HasSuffix(strings.ToLower(display[strings.LastIndexByte(display, '@')+1:]), ".test")
}

func (f *PersonalFixture) ReadEntitlement(ctx context.Context, tx *gorm.DB, user int64, incarnation string) (store.PersonalEntitlement, error) {
	if !personalSyntheticAccount(tx.WithContext(ctx), user, incarnation) {
		return store.PersonalEntitlement{}, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.now().Before(f.settings.Expires) {
		clear(f.grants)
		return store.PersonalEntitlement{}, nil
	}
	return f.grants[incarnation], nil
}

func (f *PersonalFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	deny := func() { http.NotFound(w, r) }
	if r.Method != http.MethodPost || r.URL.RawQuery != "" || r.Header.Get("Origin") != f.settings.Origin || !f.now().Before(f.settings.Expires) {
		deny()
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || len(token) != 43 {
		deny()
		return
	}
	value, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(value) != 32 {
		deny()
		return
	}
	hash := sha256.Sum256(value)
	if subtle.ConstantTimeCompare(hash[:], f.settings.CapabilityHash[:]) != 1 {
		deny()
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		deny()
		return
	}
	var input struct {
		AccountID       string `json:"accountId"`
		AccountInstance string `json:"accountInstance"`
		State           string `json:"state"`
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2048))
	if err != nil || jsonv2.Unmarshal(payload, &input, jsonv2.RejectUnknownMembers(true)) != nil {
		deny()
		return
	}
	user, err := strconv.ParseInt(input.AccountID, 10, 64)
	if err != nil || user < 1 || strconv.FormatInt(user, 10) != input.AccountID || len(input.AccountInstance) != 32 {
		deny()
		return
	}
	var state store.PersonalEntitlement
	switch input.State {
	case "premium":
		state = store.PersonalEntitlement{Premium: true, ExpiresUnix: f.settings.Expires.Unix()}
	case "expired":
		state = store.PersonalEntitlement{Premium: true, ExpiresUnix: f.now().Add(-time.Second).Unix()}
	case "free":
	default:
		deny()
		return
	}
	err = f.store.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if !personalSyntheticAccount(tx, user, input.AccountInstance) {
			return store.ErrPersonalNotFound
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.now().Before(f.settings.Expires) {
			clear(f.grants)
			return store.ErrPersonalNotFound
		}
		if input.State == "free" {
			delete(f.grants, input.AccountInstance)
			return nil
		}
		if _, exists := f.grants[input.AccountInstance]; !exists && len(f.grants) >= 256 {
			return store.ErrPersonalPermission
		}
		f.grants[input.AccountInstance] = state
		return nil
	})
	if err != nil {
		deny()
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
