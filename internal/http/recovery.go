package httpapi

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"maps"
	"net/http"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/store"
)

const (
	requestRecoveryPath   = "/api/v1/password-recovery/request"
	completeRecoveryPath  = "/api/v1/password-recovery/complete"
	recoveryIntakeBudget  = 200 * time.Millisecond
	recoveryResponseFloor = 250 * time.Millisecond
	maxRecoveryProofBytes = 1024
)

// recoveryResponse holds only the small, server-generated JSON response.
type recoveryResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (p *recoveryResponse) Header() http.Header { return p.header }
func (p *recoveryResponse) WriteHeader(status int) {
	if p.status == 0 {
		p.status = status
	}
}
func (p *recoveryResponse) Write(b []byte) (int, error) {
	p.WriteHeader(http.StatusOK)
	return p.body.Write(b)
}

func (p *recoveryResponse) flush(w http.ResponseWriter) {
	maps.Copy(w.Header(), p.header)
	w.WriteHeader(p.status)
	_, _ = p.body.WriteTo(w)
}

func (a *API) recoveryRoute(padded bool, next http.HandlerFunc) http.HandlerFunc {
	guard := a.guard(false, func(w http.ResponseWriter, r *http.Request) {
		if info(r).Session.UserID != nil {
			a.fail(w, r, http.StatusConflict, "sign_out_required", "Sign out before using password recovery.")
			return
		}
		next(w, r)
	})
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		p := recoveryResponse{header: make(http.Header)}
		if padded {
			func() {
				deadline := start.Add(recoveryIntakeBudget)
				ctx, cancel := context.WithDeadline(r.Context(), deadline)
				defer cancel()
				release := a.Config.Recovery.Work.BeginIntake()
				defer release()
				// Context deadlines cover SQLite but don't interrupt a slow upload.
				control := http.NewResponseController(w)
				if control.SetReadDeadline(deadline) == nil {
					defer func() { _ = control.SetReadDeadline(time.Time{}) }()
				}
				guard(&p, r.WithContext(ctx))
				if !time.Now().Before(deadline) && p.status != http.StatusAccepted && p.status != http.StatusTooManyRequests && p.status != http.StatusServiceUnavailable {
					p = recoveryResponse{header: make(http.Header)}
					a.err(&p, r, context.DeadlineExceeded)
				}
			}()
			timer := time.NewTimer(time.Until(start.Add(recoveryResponseFloor)))
			defer timer.Stop()
			select {
			case <-r.Context().Done():
				return
			case <-timer.C:
			}
		} else {
			guard(&p, r)
		}
		p.header.Del("X-MyChef-User")
		p.header.Del("Set-Cookie")
		p.flush(w)
	}
}

func (a *API) requestRecovery(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	wait, err := a.Config.Recovery.Request(r.Context(), emailClientAddress(r, a.Config.TrustedProxies), body.Email)
	switch {
	case err != nil:
		a.err(w, r, err)
	case wait != 0:
		a.emailRetry(w, r, wait)
	default:
		a.write(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	}
}

func (a *API) completeRecovery(w http.ResponseWriter, r *http.Request) {
	var body map[string]jsontext.Value
	var proof, password *string
	invalidLink := func() {
		a.fail(w, r, http.StatusBadRequest, "invalid_link", "This reset link is invalid or has expired. Request a new one.")
	}
	decoded := recoveryResponse{header: make(http.Header)}
	decodedOK := a.decodeCredentials(&decoded, r, &body)
	if raw := body["proof"]; decodedOK && raw != nil {
		if err := jsonv2.Unmarshal(raw, &proof); err != nil {
			proof = nil
		}
	}
	passwordOK := decodedOK && jsonv2.Unmarshal(body["password"], &password) == nil && password != nil
	for name := range body {
		if name != "proof" && name != "password" {
			passwordOK = false
		}
	}
	recovery := a.Config.Recovery
	source, err := store.EmailRateBucket(recovery.RateKey, "recovery-completion/source", emailClientAddress(r, a.Config.TrustedProxies))
	if err != nil {
		a.err(w, r, err)
		return
	}
	var proofBucket string
	if proof != nil && len(*proof) <= maxRecoveryProofBytes {
		proofBucket, err = recovery.RecoveryCompletionBucket(*proof)
		if err != nil {
			a.err(w, r, err)
			return
		}
	}
	now := time.Now().UTC()
	if recovery.Now != nil {
		now = recovery.Now().UTC()
	}
	wait, err := a.Store.ReserveRecoveryCompletion(r.Context(), now, source, proofBucket)
	switch {
	case err != nil:
		a.err(w, r, err)
	case wait != 0:
		a.emailRetry(w, r, wait)
	case !decodedOK:
		decoded.flush(w)
	case proofBucket == "":
		invalidLink()
	case !passwordOK:
		a.fail(w, r, http.StatusBadRequest, "invalid_input", "Check the request fields and size.")
	default:
		err := recovery.Complete(r.Context(), info(r).Token, *proof, *password)
		switch {
		case errors.Is(err, auth.ErrRecoveryProof):
			invalidLink()
		case err != nil:
			a.err(w, r, err)
		default:
			a.write(w, http.StatusOK, map[string]string{"status": "reset"})
		}
	}
}
