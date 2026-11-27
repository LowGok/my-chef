package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/store"
)

const nativeAppPrefix = "/api/native/app/v1/"

func nativeSyncRequest(r *http.Request, suffix string) bool {
	if r.Method == http.MethodPost && suffix == "refresh" {
		return true
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		return false
	}
	week, ok := strings.CutPrefix(suffix, "shopping/")
	if !ok || len(week) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", week)
	return err == nil
}

// The fixed route selects a lane. It never trusts a client routing header.
// Keep the read fence until the handler returns so an acknowledged withdrawal
// cannot be followed by a previously admitted mutation.
func (a *API) admitNativeIdentity(w http.ResponseWriter, r *http.Request) (*http.Request, func(), bool) {
	if !strings.HasPrefix(r.URL.Path, nativeAppPrefix) {
		return nativeRequest(r, nativePrefix, "preview"), func() {}, true
	}
	if !a.Config.NativeAppIngress {
		a.fail(w, r, http.StatusNotFound, "not_found", "That resource was not found.")
		return r, func() {}, false
	}
	a.Store.NativeIdentityWork.RLock()
	release := a.Store.NativeIdentityWork.RUnlock
	policy, err := a.Store.NativeIdentityPolicy(r.Context())
	if err != nil {
		a.fail(w, r, 503, "native_identity_unavailable", "This app is temporarily unavailable.")
		return r, release, false
	}
	suffix := strings.TrimPrefix(r.URL.Path, nativeAppPrefix)
	if suffix == "identity-status" && r.Method == http.MethodGet {
		a.write(w, 200, map[string]bool{"active": policy.Active})
		return r, release, false
	}
	revision, deadline := policy.Revision, int64(0)
	if !policy.Active {
		if policy.SyncUntil <= time.Now().Unix() || !nativeSyncRequest(r, suffix) {
			a.fail(w, r, 503, "native_identity_paused", "This app is paused. Your saved changes remain on this device.")
			return r, release, false
		}
		revision, deadline = policy.SyncRevision, policy.SyncUntil
	}
	r = nativeRequest(r, nativeAppPrefix, "app")
	ctx := store.WithNativeIdentityAuthority(r.Context(), revision)
	r = r.WithContext(auth.WithNativeIdentityPolicy(ctx, revision, deadline))
	return r, release, true
}
