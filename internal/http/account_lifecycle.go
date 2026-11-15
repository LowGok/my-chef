package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/billing"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

type accountActionInput struct {
	CurrentPassword string `json:"currentPassword"`
}

func (a *API) accountAction(w http.ResponseWriter, r *http.Request, password string) (store.AccountActor, bool) {
	if a.Config.Lifecycle == nil {
		a.fail(w, r, 503, "account_service_unavailable", "Account services are temporarily unavailable.")
		return store.AccountActor{}, false
	}
	current := info(r)
	finish, retry := a.passwordLimits.begin(*current.Session.UserID, emailClientAddress(r, a.Config.TrustedProxies))
	if finish == nil {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		a.fail(w, r, 429, "rate_limited", "Too many attempts. Wait and try again.")
		return store.AccountActor{}, false
	}
	actor, err := a.Auth.VerifyAccountAction(r.Context(), *current.Session.UserID, current.Token, password)
	outcome := passwordUncounted
	switch {
	case errors.Is(err, auth.ErrCurrentPassword):
		outcome = passwordWrong
		a.fail(w, r, 400, "current_password_incorrect", "Check your current password and try again.")
	case err != nil:
		a.accountActionError(w, r, err)
	default:
		w.Header().Set("X-MyChef-User", strconv.FormatInt(actor.UserID, 10))
	}
	finish(outcome)
	return actor, err == nil
}
func (a *API) accountActionError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrAccountAccess):
		a.err(w, r, auth.ErrSession)
	case errors.Is(err, store.ErrAccountConfirmation):
		a.fail(w, r, 400, "invalid_confirmation", "Type DELETE exactly to confirm.")
	case errors.Is(err, store.ErrLifecycleConflict):
		a.fail(w, r, 409, "deletion_conflict", "Refresh the account page before trying again.")
	default:
		a.err(w, r, err)
	}
}

type accountExportWriter struct {
	http.ResponseWriter
	started bool
}

func (w *accountExportWriter) Write(data []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(data)
}

// The server also applies its thirty-second WriteTimeout. ResponseController
// applies the earlier request deadline and interrupts a write on cancellation.
func (w *accountExportWriter) WriteContext(ctx context.Context, data []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	controller := http.NewResponseController(w.ResponseWriter)
	if deadline, ok := ctx.Deadline(); ok {
		if err := controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return 0, err
		}
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = controller.SetWriteDeadline(time.Now()); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	return w.Write(data)
}
func (a *API) exportAccount(w http.ResponseWriter, r *http.Request) {
	var input accountActionInput
	if !a.decodeCredentials(w, r, &input) {
		return
	}
	actor, ok := a.accountAction(w, r, input.CurrentPassword)
	if !ok {
		return
	}
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		a.accountActionError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="mychef-data-`+hex.EncodeToString(suffix[:])+`.zip"`)
	writer := &accountExportWriter{ResponseWriter: w}
	options := store.AccountExportOptions{ImportVault: a.retainedImportVault(), ReadPhoto: func(ctx context.Context, asset store.PersonalPhotoAsset) ([]byte, error) {
		reader := a.Config.PersonalPhotos
		if reader == nil || reader.AssetBinding() != asset.Binding || asset.DeliveryType != "authenticated" {
			return nil, store.ErrPersonalStorage
		}
		return reader.ReadPersonalPhoto(ctx, asset.Target, asset.Digest)
	}}
	if cfg := a.Config.NativeBilling; cfg != nil {
		options.Billing = a.Config.Lifecycle
		options.BillingEnvironment = cfg.Environment
	}
	if err := a.Store.WriteAccountExport(r.Context(), actor, writer, time.Now().UTC(), options); err != nil {
		if writer.started {
			// Do not complete a clean HTTP transfer of an incomplete archive.
			// The server closes this stream; no request content is logged.
			panic(http.ErrAbortHandler)
		}
		w.Header().Del("Content-Disposition")
		a.accountActionError(w, r, err)
	}
}

func (a *API) accountDeletionAdvice(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	stores := []billing.Store{}
	user := *info(r).Session.UserID
	w.Header().Set("X-MyChef-User", strconv.FormatInt(user, 10))
	if cfg := a.Config.NativeBilling; cfg != nil {
		if a.Config.Lifecycle == nil {
			a.billingError(w, r, billing.ErrStoreUnavailable)
			return
		}
		var err error
		stores, err = a.Config.Lifecycle.BillingDeletionStores(r.Context(), user, cfg.Environment, time.Now().UTC())
		if err != nil {
			a.accountActionError(w, r, err)
			return
		}
	}
	a.write(w, http.StatusOK, map[string][]billing.Store{"stores": stores})
}
func (a *API) deleteAccount(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		Confirmation    string `json:"confirmation"`
	}
	if !a.decodeCredentials(w, r, &input) {
		return
	}
	operation := r.Header.Get("Idempotency-Key")
	decoded, err := hex.DecodeString(operation)
	if err != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != operation || input.Confirmation != "DELETE" {
		a.accountActionError(w, r, store.ErrAccountConfirmation)
		return
	}
	actor, ok := a.accountAction(w, r, input.CurrentPassword)
	if !ok {
		return
	}
	now := time.Now().UTC()
	opts := store.AccountDeletionOptions{
		Cleanup: func(tx *gorm.DB, u store.User) error { return a.Auth.EraseAccountLinks(r.Context(), tx, u, now) },
		Capture: func(tx *gorm.DB, u store.User, inc string) error {
			return a.Config.Lifecycle.CaptureAccountTraces(r.Context(), tx, u.ID, inc, a.Config.TraceDir)
		},
	}
	if a.Config.AccountMail {
		opts.FinalNotice = func(tx *gorm.DB, u store.User, op string) error {
			_, err := mailqueue.Enqueue(r.Context(), a.Store, tx, now, mailqueue.Request{OwnerID: u.ID, Key: "account-deleted/" + op, Recipient: u.Email, Template: mailqueue.AccountDeletedTemplate, Fields: map[string]string{"timestamp": now.Format(time.RFC3339)}})
			return err
		}
	}
	result, err := a.Config.Lifecycle.DeleteAccount(r.Context(), actor, operation, input.Confirmation, now, opts)
	if err != nil {
		a.accountActionError(w, r, err)
		return
	}
	if !info(r).Native {
		http.SetCookie(w, &http.Cookie{Name: "mychef_session", Path: "/", HttpOnly: true, Secure: a.Config.Secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	}
	a.write(w, 200, map[string]string{"status": "deleted", "operation": result.Operation})
}
