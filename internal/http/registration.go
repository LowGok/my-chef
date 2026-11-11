package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/store"
)

func (a *API) requireAnonymousSignUp(w http.ResponseWriter, r *http.Request) bool {
	if info(r).Session.UserID != nil {
		a.fail(w, r, http.StatusConflict, "sign_out_required", "Sign out before opening a sign-up link.")
		return false
	}
	return true
}

func (a *API) emailRetry(w http.ResponseWriter, r *http.Request, wait int) {
	w.Header().Set("Retry-After", strconv.Itoa(wait))
	a.fail(w, r, http.StatusTooManyRequests, "rate_limited", "Too many requests. Wait and try again.")
}

func (a *API) signUpRequest(w http.ResponseWriter, r *http.Request) {
	if !a.requireAnonymousSignUp(w, r) {
		return
	}
	var body auth.SignUpRequest
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	wait, err := a.Config.SignUp.Request(r.Context(), emailClientAddress(r, a.Config.TrustedProxies), body)
	switch {
	case err != nil:
		a.err(w, r, err)
	case wait != 0:
		a.emailRetry(w, r, wait)
	default:
		a.write(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	}
}

func (a *API) reserveEmailProof(w http.ResponseWriter, r *http.Request, now time.Time, token string) (reservation, tokenBucket string, ok bool) {
	source := emailClientAddress(r, a.Config.TrustedProxies)
	sourceBucket, err := store.EmailRateBucket(a.Config.EmailRateKey, "proof/source", source)
	if err != nil {
		a.err(w, r, err)
		return "", "", false
	}
	tokenBucket, err = store.EmailRateBucket(a.Config.EmailRateKey, "proof/token", auth.Hash(token))
	if err != nil {
		a.err(w, r, err)
		return "", "", false
	}
	result, err := a.Store.ReserveEmailLimits(r.Context(), nil, now,
		store.EmailLimit{Bucket: sourceBucket, Maximum: 50, Window: 15 * time.Minute},
		store.EmailLimit{Bucket: tokenBucket, Maximum: 10, Window: 15 * time.Minute})
	if err != nil {
		a.err(w, r, err)
		return "", "", false
	}
	if result.RetryAfter != 0 {
		a.emailRetry(w, r, result.RetryAfter)
		return "", "", false
	}
	return result.Reservation, tokenBucket, true
}

func (a *API) signUpTime() time.Time {
	if a.Config.SignUp.Now != nil {
		return a.Config.SignUp.Now().UTC()
	}
	return time.Now().UTC()
}

func (a *API) signUpError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, auth.ErrSignUpProof) {
		a.fail(w, r, http.StatusBadRequest, "invalid_link", "This sign-up link is no longer valid. Request another email.")
		return
	}
	a.err(w, r, err)
}

func (a *API) signUpInspect(w http.ResponseWriter, r *http.Request) {
	if !a.requireAnonymousSignUp(w, r) {
		return
	}
	var body struct {
		Proof string `json:"proof"`
	}
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	reservation, bucket, ok := a.reserveEmailProof(w, r, a.signUpTime(), body.Proof)
	if !ok {
		return
	}
	details, err := a.Config.SignUp.Inspect(r.Context(), body.Proof)
	if err != nil {
		a.signUpError(w, r, err)
		return
	}
	if err := a.Store.ReleaseEmailLimit(r.Context(), nil, reservation, bucket); err != nil {
		a.err(w, r, err)
		return
	}
	a.write(w, http.StatusOK, details)
}

func (a *API) signUpComplete(w http.ResponseWriter, r *http.Request) {
	if !a.requireAnonymousSignUp(w, r) {
		return
	}
	var body auth.SignUpCompletion
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	_, bucket, ok := a.reserveEmailProof(w, r, a.signUpTime(), body.Proof)
	if !ok {
		return
	}
	if err := a.Config.SignUp.Complete(r.Context(), info(r).Token, body); err != nil {
		a.signUpError(w, r, err)
		return
	}
	_ = a.Store.ClearEmailLimit(r.Context(), nil, bucket)
	a.write(w, http.StatusCreated, map[string]string{"status": "created"})
}
