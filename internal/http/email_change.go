package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
)

func (a *API) startEmailChange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email           string `json:"email"`
		CurrentPassword string `json:"currentPassword"`
	}
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	current := info(r)
	user, err := a.liveSessionUser(r.Context(), *current.Session.UserID, current.Token)
	if err != nil {
		a.err(w, r, err)
		return
	}
	if user.EmailVerifiedAt != nil {
		w.Header().Set("X-MyChef-User", strconv.FormatInt(user.ID, 10))
	}
	wait, err := a.Config.EmailChange.Start(r.Context(), *current.Session.UserID, current.Token,
		emailClientAddress(r, a.Config.TrustedProxies), body.Email, body.CurrentPassword)
	switch {
	case errors.Is(err, auth.ErrCurrentPassword):
		a.fail(w, r, http.StatusBadRequest, "current_password_incorrect", "Check your current password and try again.")
	case err != nil:
		a.err(w, r, err)
	case wait != 0:
		a.emailRetry(w, r, wait)
	default:
		a.write(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	}
}

func (a *API) emailChangeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, auth.ErrEmailProof) {
		a.fail(w, r, http.StatusBadRequest, "invalid_link", "This email-change link is no longer valid. Start a new request in MyChef.")
		return
	}
	a.err(w, r, err)
}

func (a *API) inspectEmailChange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Proof string `json:"proof"`
	}
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	reservation, bucket, ok := a.reserveEmailProof(w, r, time.Now().UTC(), body.Proof)
	if !ok {
		return
	}
	view, err := a.Config.EmailChange.Inspect(r.Context(), body.Proof)
	if err != nil {
		a.emailChangeError(w, r, err)
		return
	}
	if err := a.Store.ReleaseEmailLimit(r.Context(), nil, reservation, bucket); err != nil {
		a.err(w, r, err)
		return
	}
	a.write(w, http.StatusOK, view)
}

func (a *API) decideEmailChange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Proof string `json:"proof"`
	}
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	_, bucket, ok := a.reserveEmailProof(w, r, time.Now().UTC(), body.Proof)
	if !ok {
		return
	}
	result, err := a.Config.EmailChange.Decide(r.Context(), body.Proof)
	if err != nil {
		a.emailChangeError(w, r, err)
		return
	}
	_ = a.Store.ClearEmailLimit(r.Context(), nil, bucket)
	reply := map[string]any{"status": result.Status}
	if owner := info(r).Session.UserID; result.Status == "changed" && owner != nil && *owner == result.UserID {
		reply["signInRequired"] = true
	}
	a.write(w, http.StatusOK, reply)
}
