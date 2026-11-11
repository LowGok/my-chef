package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func (a *API) liveSessionUser(ctx context.Context, userID int64, token string) (store.User, error) {
	var user store.User
	err := a.Store.DB.WithContext(ctx).Table("users").Select("users.*").
		Joins("JOIN sessions ON sessions.user_id = users.user_id").
		Where("users.user_id = ? AND sessions.hash = ? AND sessions.expires_unix > ?",
			userID, auth.Hash(token), time.Now().Unix()).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return user, auth.ErrSession
	}
	return user, err
}

func (a *API) guardVerified(next http.HandlerFunc) http.HandlerFunc {
	if a.Config.Verification == nil {
		return a.guard(true, next)
	}
	return a.guard(true, func(w http.ResponseWriter, r *http.Request) {
		current := info(r)
		user, err := a.liveSessionUser(r.Context(), *current.Session.UserID, current.Token)
		if err != nil {
			a.err(w, r, err)
			return
		}
		if user.EmailVerifiedAt == nil {
			a.fail(w, r, http.StatusForbidden, "email_verification_required", "Verify your email to use MyChef.")
			return
		}
		w.Header().Set("X-MyChef-User", strconv.FormatInt(user.ID, 10))
		next(w, r)
	})
}

func (a *API) sessionResponse(w http.ResponseWriter, status int, token string, user *store.Profile, restricted bool) {
	response := map[string]any{"user": user, "csrfToken": auth.CSRF(token)}
	if restricted {
		response["verificationRequired"] = true
	}
	a.write(w, status, response)
}

func (a *API) resendEmailVerification(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	current := info(r)
	wait, err := a.Config.Verification.Resend(r.Context(), *current.Session.UserID, current.Token,
		emailClientAddress(r, a.Config.TrustedProxies))
	switch {
	case err != nil:
		a.err(w, r, err)
	case wait != 0:
		a.emailRetry(w, r, wait)
	default:
		a.write(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	}
}

func (a *API) confirmEmailVerification(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Proof string `json:"proof"`
	}
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	current := info(r)
	_, bucket, ok := a.reserveEmailProof(w, r, time.Now().UTC(), body.Proof)
	if !ok {
		return
	}
	user, replacement, err := a.Config.Verification.Confirm(r.Context(), *current.Session.UserID, current.Token, body.Proof)
	switch {
	case errors.Is(err, auth.ErrEmailProof):
		a.fail(w, r, http.StatusBadRequest, "invalid_link", "This verification link is no longer valid for this session. Sign in to the matching account or request another email.")
	case err != nil:
		a.err(w, r, err)
	default:
		_ = a.Store.ClearEmailLimit(r.Context(), nil, bucket)
		a.signedIn(w, r, user, replacement, http.StatusOK)
	}
}
