package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
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
	lease    store.FixtureEntitlementLease
}

func NewPersonalFixture(db *store.Store, settings config.FixtureInbox, now func() time.Time) *PersonalFixture {
	if now == nil {
		now = time.Now
	}
	hash := sha256.New()
	hash.Write(settings.CapabilityHash[:])
	hash.Write([]byte(settings.Expires.UTC().Format(time.RFC3339Nano)))
	lease := store.FixtureEntitlementLease{ID: hex.EncodeToString(hash.Sum(nil)), Expires: settings.Expires}
	return &PersonalFixture{store: db, settings: settings, now: now, lease: lease}
}

func (f *PersonalFixture) ReadEntitlement(ctx context.Context, tx *gorm.DB, user int64, incarnation string) (store.PersonalEntitlement, error) {
	return store.EntitlementReader(f.lease, f.now)(ctx, tx, user, incarnation)
}

func (f *PersonalFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	deny := func() { http.NotFound(w, r) }
	if (r.Method != http.MethodGet && r.Method != http.MethodPost) || r.URL.RawQuery != "" || r.Header.Get("Origin") != f.settings.Origin || !f.now().Before(f.settings.Expires) {
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
	if r.Method == http.MethodGet {
		generation, err := f.store.FixtureEntitlementGeneration(r.Context(), f.lease, f.now)
		if err != nil {
			deny()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Generation string `json:"generation"`
		}{Generation: generation})
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
		Generation      string `json:"generation"`
		OperationID     string `json:"operationId"`
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
	err = f.store.SetFixtureEntitlement(r.Context(), f.lease, store.FixtureEntitlementOperation{
		ID: input.OperationID, Generation: input.Generation, Incarnation: input.AccountInstance,
		State: input.State, UserID: user,
	}, f.now)
	if err != nil {
		deny()
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
