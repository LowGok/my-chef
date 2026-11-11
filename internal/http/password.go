package httpapi

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/LowGok/my-chef/internal/auth"
)

// Credential requests reject malformed Unicode rather than silently replacing
// it. Keep decoder errors private because they can contain request text.
func (a *API) decodeCredentials(w http.ResponseWriter, r *http.Request, dst any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		a.fail(w, r, http.StatusBadRequest, "invalid_input", "Send a JSON request.")
		return false
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
	if err != nil || jsonv2.Unmarshal(payload, dst, jsonv2.RejectUnknownMembers(true)) != nil {
		a.fail(w, r, http.StatusBadRequest, "invalid_input", "Check the request fields, encoding and size.")
		return false
	}
	return true
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	current := info(r)
	finish, retry := a.passwordLimits.begin(*current.Session.UserID, address(r))
	if finish == nil {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		a.fail(w, r, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Wait and try again.")
		return
	}
	outcome := passwordUncounted
	defer func() { finish(outcome) }()
	var body struct {
		Current string `json:"currentPassword"`
		New     string `json:"newPassword"`
	}
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	user, token, err := a.Auth.ChangePassword(r.Context(), *current.Session.UserID, current.Token, body.Current, body.New)
	switch {
	case errors.Is(err, auth.ErrCurrentPassword):
		outcome = passwordWrong
		a.fail(w, r, http.StatusBadRequest, "current_password_incorrect", "Check your current password and try again.")
	case errors.Is(err, auth.ErrPasswordReuse):
		a.fail(w, r, http.StatusBadRequest, "password_reused", "Choose a password different from your current one.")
	case err != nil:
		a.err(w, r, err)
	default:
		outcome = passwordChanged
		a.signedIn(w, r, user, token, http.StatusOK)
	}
}
