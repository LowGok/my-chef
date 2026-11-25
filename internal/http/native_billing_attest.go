package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
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
	Verifier interface {
		billing.NativeCancellationVerifier
		VerifyLaunch(context.Context, billing.NativeCancellation, []byte) error
	}
	// Process-local authority. Restart discards unused launch authorizations.
	LaunchKey [32]byte
}

func (b *NativeBilling) appleAttestationEnabled() bool {
	return b.Environment == billing.Sandbox && b.SyntheticAccountsOnly && b.AppleAttestation != nil &&
		b.AppleAttestation.Registry != nil && b.AppleAttestation.Verifier != nil && b.AppleAttestation.LaunchKey != ([32]byte{})
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

const appleLaunchAuthorizationTTL = 2 * time.Minute
const appleLaunchAuthorizationBytes = 72

func appleLaunchAuthorizationMAC(key [32]byte, c billing.NativeCancellation, prefix []byte) []byte {
	a := c.Attempt
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte("mychef.storekit.launch-authorization.v1\x00" + string(a.Environment) + "\x00" +
		a.ID + "\x00" + a.Binding + "\x00" + string(a.Interval) + "\x00" + c.Owner + "\x00" + c.Family + "\x00"))
	mac.Write(c.AppInstance[:])
	mac.Write(prefix)
	return mac.Sum(nil)
}

func validAppleLaunchAuthorization(key [32]byte, c billing.NativeCancellation, wire []byte, now time.Time) bool {
	if len(wire) != appleLaunchAuthorizationBytes || key == ([32]byte{}) ||
		!hmac.Equal(wire[40:], appleLaunchAuthorizationMAC(key, c, wire[:40])) {
		return false
	}
	issued := time.Unix(int64(binary.BigEndian.Uint64(wire[32:40])), 0)
	return !issued.After(now) && now.Before(issued.Add(appleLaunchAuthorizationTTL))
}

func (a *API) appleLaunchClaim(r *http.Request) (billing.NativeCancellation, error) {
	b := a.Config.NativeBilling
	owner, attempt, err := a.billingAttempt(r)
	if err != nil {
		return billing.NativeCancellation{}, err
	}
	if attempt.Store != billing.Apple || !b.appleAttestationEnabled() || !b.checkoutAllowed(attempt.Store) {
		return billing.NativeCancellation{}, billing.ErrPurchaseBinding
	}
	family := info(r).Family
	key, err := b.AppleAttestation.Registry.EnrolledInstance(r.Context(), owner, family)
	if err != nil {
		return billing.NativeCancellation{}, err
	}
	return billing.NativeCancellation{Attempt: attempt, Owner: owner, Family: family, AppInstance: key}, nil
}

func (a *API) billingAppleLaunchChallenge(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	if r.ContentLength != 0 {
		a.personalError(w, r, store.ErrPersonalInput)
		return
	}
	claim, err := a.appleLaunchClaim(r)
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	if !a.throttle(w, r, "billing-apple-launch-challenge:"+claim.Owner, 8) {
		return
	}
	var wire [appleLaunchAuthorizationBytes]byte
	if _, err := rand.Read(wire[:32]); err != nil {
		a.billingError(w, r, err)
		return
	}
	binary.BigEndian.PutUint64(wire[32:40], uint64(time.Now().Unix()))
	copy(wire[40:], appleLaunchAuthorizationMAC(a.Config.NativeBilling.AppleAttestation.LaunchKey, claim, wire[:40]))
	a.write(w, http.StatusOK, map[string]string{"authorization": base64.RawURLEncoding.EncodeToString(wire[:])})
}

func (a *API) billingAppleAuthorizedLaunch(w http.ResponseWriter, r *http.Request) {
	claim, err := a.appleLaunchClaim(r)
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	var body struct {
		Authorization string `json:"authorization"`
		Assertion     string `json:"assertion"`
	}
	if !a.decodePersonal(w, r, &body, 2<<10) {
		return
	}
	wire, wellFormed := parseAppleBillingProof(body.Authorization, appleLaunchAuthorizationBytes)
	proof, proofWellFormed := parseAppleBillingProof(body.Assertion, 1024)
	if !wellFormed || len(wire) != appleLaunchAuthorizationBytes || !proofWellFormed {
		a.personalError(w, r, store.ErrPersonalInput)
		return
	}
	b := a.Config.NativeBilling
	attestation := b.AppleAttestation
	if !validAppleLaunchAuthorization(attestation.LaunchKey, claim, wire, time.Now()) {
		a.billingError(w, r, billing.ErrPurchaseBinding)
		return
	}
	offer, _, ok := b.offer(billing.Apple, claim.Attempt.Interval)
	if !ok {
		a.billingError(w, r, billing.ErrPurchaseBinding)
		return
	}
	if !a.throttle(w, r, "billing-apple-launch:"+claim.Owner, 12) {
		return
	}
	copy(claim.Challenge[:], wire[:32])
	if err := attestation.Verifier.VerifyLaunch(r.Context(), claim, proof); err != nil {
		a.billingError(w, r, billing.ErrPurchaseBinding)
		return
	}
	var cancellation billing.NativeCancellation
	err = a.appleBillingFence(r, claim.Owner, claim.Family)(func() error {
		now := time.Now()
		if !b.checkoutAllowed(billing.Apple) || !validAppleLaunchAuthorization(attestation.LaunchKey, claim, wire, now) {
			return billing.ErrPurchaseBinding
		}
		var err error
		cancellation, err = b.Journal.LaunchForNativeCancellation(r.Context(), claim.Attempt, claim.Owner, claim.Family, claim.AppInstance, now)
		return err
	})
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	// The journal consumes the reservation atomically. No second proof can
	// launch that checkout and neither proof can recreate a cancelled one.
	out := appleChallengeResponse(cancellation.Challenge)
	out["checkout"], out["binding"], out["product"], out["interval"] =
		claim.Attempt.ID, claim.Attempt.Binding, offer.Product, string(claim.Attempt.Interval)
	a.write(w, http.StatusOK, out)
}
