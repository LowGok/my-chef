package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/LowGok/my-chef/internal/billing"
	"github.com/LowGok/my-chef/internal/store"
)

type appleBillingRegistry interface {
	BeginEnrollment(context.Context, string, string, time.Time) ([32]byte, error)
	Enroll(context.Context, string, string, [32]byte, [32]byte, []byte, func(func() error) error, func() time.Time) error
	EnrolledInstance(context.Context, string, string) ([32]byte, error)
}

// AppleBillingAttestation is configured by the server for the isolated sandbox.
// The client never selects its verifier, environment, or enrolled instance.
type AppleBillingAttestation struct {
	Registry appleBillingRegistry
	Verifier billing.NativeCancellationVerifier
}

func (b *NativeBilling) appleAttestationEnabled() bool {
	return b.Environment == billing.Sandbox && b.SyntheticAccountsOnly && b.AppleAttestation != nil &&
		b.AppleAttestation.Registry != nil && b.AppleAttestation.Verifier != nil
}

func appleChallengeResponse(challenge [32]byte) map[string]string {
	return map[string]string{"challenge": base64.RawURLEncoding.EncodeToString(challenge[:])}
}

func parseAppleBillingProof(value string, max int) ([]byte, bool) {
	encoding := base64.RawURLEncoding.Strict()
	if len(value) == 0 || len(value) > encoding.EncodedLen(max) {
		return nil, false
	}
	b, err := encoding.DecodeString(value)
	return b, err == nil && len(b) <= max && len(value) == encoding.EncodedLen(len(b))
}

func appleBillingID(value string) ([32]byte, bool) {
	b, valid := parseAppleBillingProof(value, 32)
	if !valid || len(b) != 32 {
		return [32]byte{}, false
	}
	return [32]byte(b), [32]byte(b) != ([32]byte{})
}

func (a *API) billingAppleActor(r *http.Request) (string, error) {
	b := a.Config.NativeBilling
	if !b.appleAttestationEnabled() {
		return "", billing.ErrPurchaseBinding
	}
	owner, platform, err := a.billingActor(r)
	if err != nil {
		return "", err
	}
	if platform != billing.Apple {
		return "", billing.ErrPurchaseBinding
	}
	return owner, nil
}

// The proof is verified outside AccountWork. Hold the live-session fence
// only for the single journal commit after verification succeeds.
func (a *API) appleBillingFence(r *http.Request, owner, family string) func(func() error) error {
	return func(commit func() error) error {
		a.Store.AccountWork.RLock()
		defer a.Store.AccountWork.RUnlock()
		current, err := a.billingAppleActor(r)
		if err != nil {
			return err
		}
		if current != owner || info(r).Family != family {
			return billing.ErrPurchaseBinding
		}
		return commit()
	}
}

// A lost enrollment response or app restart does not allocate another
// key. The current verified Apple session can read only its own identifier.
func (a *API) billingAppleEnrollmentState(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	if r.ContentLength != 0 {
		a.personalError(w, r, store.ErrPersonalInput)
		return
	}
	a.Store.AccountWork.RLock()
	defer a.Store.AccountWork.RUnlock()
	owner, err := a.billingAppleActor(r)
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	key, err := a.Config.NativeBilling.AppleAttestation.Registry.EnrolledInstance(r.Context(), owner, info(r).Family)
	if errors.Is(err, billing.ErrPurchaseBinding) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	a.write(w, http.StatusOK, map[string]string{"keyID": base64.RawURLEncoding.EncodeToString(key[:])})
}

func (a *API) billingAppleEnrollmentChallenge(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	if r.ContentLength != 0 {
		a.personalError(w, r, store.ErrPersonalInput)
		return
	}
	a.Store.AccountWork.RLock()
	defer a.Store.AccountWork.RUnlock()
	owner, err := a.billingAppleActor(r)
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	if !a.Config.NativeBilling.checkoutAllowed(billing.Apple) {
		a.billingError(w, r, billing.ErrPurchaseBinding)
		return
	}
	if !a.throttle(w, r, "billing-apple-challenge:"+owner, 6) {
		return
	}
	challenge, err := a.Config.NativeBilling.AppleAttestation.Registry.BeginEnrollment(r.Context(), owner, info(r).Family, time.Now())
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	a.write(w, http.StatusOK, appleChallengeResponse(challenge))
}

func (a *API) billingAppleEnroll(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	var body struct {
		Challenge   string `json:"challenge"`
		KeyID       string `json:"keyID"`
		Attestation string `json:"attestation"`
	}
	if !a.decodePersonal(w, r, &body, 90<<10) {
		return
	}
	challenge, validChallenge := appleBillingID(body.Challenge)
	key, validKey := appleBillingID(body.KeyID)
	proof, validProof := parseAppleBillingProof(body.Attestation, 64<<10)
	if !validChallenge || !validKey || !validProof {
		a.personalError(w, r, store.ErrPersonalInput)
		return
	}
	owner, err := a.billingAppleActor(r)
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	if !a.throttle(w, r, "billing-apple-enroll:"+owner, 6) {
		return
	}
	family := info(r).Family
	err = a.Config.NativeBilling.AppleAttestation.Registry.Enroll(r.Context(), owner, family, challenge, key, proof,
		a.appleBillingFence(r, owner, family), time.Now)
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) billingAppleCancel(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	var body struct {
		Challenge string `json:"challenge"`
		Assertion string `json:"assertion"`
	}
	if !a.decodePersonal(w, r, &body, 2<<10) {
		return
	}
	challenge, validChallenge := appleBillingID(body.Challenge)
	proof, validProof := parseAppleBillingProof(body.Assertion, 1024)
	if !validChallenge || !validProof {
		a.personalError(w, r, store.ErrPersonalInput)
		return
	}
	owner, err := a.billingAppleActor(r)
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	// NativeAttempt matches this account, native session family, and store.
	b := a.Config.NativeBilling
	family := info(r).Family
	attempt, err := b.Journal.NativeAttempt(r.Context(), owner, family, r.PathValue("checkout"), b.Environment, billing.Apple)
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	if !a.throttle(w, r, "billing-apple-cancel:"+owner, 12) {
		return
	}
	instance, err := b.AppleAttestation.Registry.EnrolledInstance(r.Context(), owner, family)
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	claim := billing.NativeCancellation{Attempt: attempt, Owner: owner, Family: family, AppInstance: instance, Challenge: challenge}
	err = b.Journal.CompleteNativeCancellation(r.Context(), claim, proof, b.AppleAttestation.Verifier,
		a.appleBillingFence(r, owner, family), time.Now)
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
