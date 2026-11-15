package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/billing"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

// NativeBilling must be supplied by the server. With a nil configuration
// there is no route, store client or purchase capability.
type NativeBilling struct {
	Journal         *billing.Journal
	Environment     billing.Environment
	CheckoutEnabled bool
	Offers          []billing.Offer
	Providers       map[billing.Store]billing.Provider
	// Play routes require the same protected inbox used for background polling.
	Play *billing.PlayInbox
	// A usable Play restore and poll path cannot enable paid checkout itself.
	PlayCheckoutEnabled     bool
	Available               func() bool
	SyntheticAccountsOnly   bool
	AppleAttestation        *AppleBillingAttestation
	RequireAppleAttestation bool
}

func (b *NativeBilling) available() bool {
	return b != nil && (b.Available == nil || b.Available())
}

func (b *NativeBilling) Entitlements(fallback store.PersonalEntitlementReader) store.PersonalEntitlementReader {
	current := store.NativeBillingEntitlementReader(b.Journal, b.Environment, fallback, nil)
	return func(ctx context.Context, tx *gorm.DB, user int64, incarnation string) (store.PersonalEntitlement, error) {
		active := b.available()
		if active && b.SyntheticAccountsOnly {
			var err error
			active, err = store.SyntheticFixtureAccount(tx.WithContext(ctx), user, incarnation)
			if err != nil {
				return store.PersonalEntitlement{}, err
			}
		}
		if active {
			return current(ctx, tx, user, incarnation)
		}
		if fallback != nil {
			return fallback(ctx, tx, user, incarnation)
		}
		return store.PersonalEntitlement{}, nil
	}
}

func (b *NativeBilling) hasPlay() bool {
	return b.Play.UsesJournal(b.Journal, b.Environment)
}

func (b *NativeBilling) checkoutAllowed(store billing.Store) bool {
	if !b.CheckoutEnabled {
		return false
	}
	switch store {
	case billing.Apple:
		return !b.RequireAppleAttestation && b.AppleAttestation == nil || b.appleAttestationEnabled()
	case billing.Play:
		return b.PlayCheckoutEnabled && b.hasPlay()
	default:
		return false
	}
}

func (b *NativeBilling) offer(s billing.Store, i billing.Interval) (billing.Offer, billing.Provider, bool) {
	for _, o := range b.Offers {
		if o.Store != s || o.Environment != b.Environment || o.Interval != i {
			continue
		}
		if s == billing.Play {
			if b.hasPlay() && b.Play.Offers(o) {
				return o, nil, true
			}
		} else if provider := b.Providers[s]; provider != nil {
			return o, provider, true
		}
	}
	return billing.Offer{}, nil, false
}

func (a *API) billingRoutes(mux *http.ServeMux) {
	if a.Config.NativeBilling == nil || a.Config.NativeBilling.Journal == nil {
		return
	}
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/billing/status":                       a.billingStatus,
		"POST /api/v1/billing/checkouts":                   a.billingReserve,
		"DELETE /api/v1/billing/checkouts/{checkout}":      a.billingReleaseUnused,
		"POST /api/v1/billing/checkouts/{checkout}/launch": a.billingLaunch,
		"POST /api/v1/billing/checkouts/{checkout}/verify": a.billingVerify,
		"POST /api/v1/billing/restore":                     a.billingRestore,
	}
	if a.Config.NativeBilling.appleAttestationEnabled() {
		routes["POST /api/v1/billing/apple/enrollment/challenge"] = a.billingAppleEnrollmentChallenge
		routes["POST /api/v1/billing/apple/enrollment"] = a.billingAppleEnroll
		routes["GET /api/v1/billing/apple/enrollment"] = a.billingAppleEnrollmentState
		routes["POST /api/v1/billing/checkouts/{checkout}/cancel"] = a.billingAppleCancel
		routes["POST /api/v1/billing/checkouts/{checkout}/launch-challenge"] = a.billingAppleLaunchChallenge
	}
	for path, handler := range routes {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if !a.Config.NativeBilling.available() {
				http.NotFound(w, r)
				return
			}
			if !info(r).Native {
				a.billingError(w, r, billing.ErrPurchaseBinding)
				return
			}
			a.guardVerified(handler)(w, r)
		})
	}
}

var errBillingEmail = errors.New("billing requires verified email")

func (a *API) billingActor(r *http.Request) (string, billing.Store, error) {
	// This also runs inside the commit fence after provider I/O.
	if !a.Config.NativeBilling.available() {
		return "", "", billing.ErrPurchaseBinding
	}
	current := info(r)
	s := billing.Apple
	switch current.Platform {
	case "ios":
	case "android":
		s = billing.Play
	default:
		return "", "", billing.ErrPurchaseBinding
	}
	u, err := a.liveSessionUser(r.Context(), *current.Session.UserID, current.Token)
	if err != nil {
		return "", "", err
	}
	if u.EmailVerifiedAt == nil || *u.EmailVerifiedAt <= 0 {
		return "", "", errBillingEmail
	}
	if a.Config.NativeBilling.SyntheticAccountsOnly && !store.IsSyntheticFixtureEmail(u.Email) {
		return "", "", billing.ErrPurchaseBinding
	}
	var id string
	err = a.Store.SQL.QueryRowContext(r.Context(), "SELECT incarnation FROM account_incarnations WHERE user_id=?", u.ID).Scan(&id)
	return id, s, err
}

func (a *API) billingError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errBillingEmail):
		a.fail(w, r, 403, "email_verification_required", "Verify your email to manage Premium.")
	case errors.Is(err, auth.ErrSession):
		a.err(w, r, auth.ErrSession)
	case errors.Is(err, billing.ErrPurchaseBinding):
		a.fail(w, r, 404, "checkout_unavailable", "That checkout is unavailable.")
	case errors.Is(err, billing.ErrAlreadyPremium), errors.Is(err, billing.ErrCheckoutBusy),
		errors.Is(err, billing.ErrUnresolved), errors.Is(err, billing.ErrStalePurchase):
		a.fail(w, r, 409, "checkout_pending", "Check your subscription or continue the previous purchase.")
	default:
		a.fail(w, r, 503, "billing_unavailable", "Your purchase could not be verified yet. Try again.")
	}
}

type nativeBillingPlan struct {
	Interval billing.Interval `json:"interval"`
	Product  string           `json:"product"`
	BasePlan string           `json:"basePlan"`
}

type nativeBillingStatus struct {
	Store                    billing.Store                   `json:"store"`
	Plans                    []nativeBillingPlan             `json:"plans"`
	CheckoutEnabled          bool                            `json:"checkoutEnabled"`
	RestoreAvailable         bool                            `json:"restoreAvailable"`
	Pending                  bool                            `json:"pending"`
	Subscription             *billing.SubscriptionSummary    `json:"subscription,omitempty"`
	Management               *billing.SubscriptionManagement `json:"management,omitempty"`
	Resume                   *billing.PendingAttempt         `json:"resume,omitempty"`
	AppleAttestationRequired bool                            `json:"appleAttestationRequired,omitempty"`
}

func (a *API) billingStatus(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	a.Store.AccountWork.RLock()
	defer a.Store.AccountWork.RUnlock()
	id, s, err := a.billingActor(r)
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	b := a.Config.NativeBilling
	result, err := b.Journal.NativeOverview(r.Context(), id, info(r).Family, b.Environment, s, time.Now())
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	_, restore := b.Providers[s].(billing.RestoreProvider)
	if s == billing.Play {
		restore = b.hasPlay()
	}
	plans := []nativeBillingPlan{}
	if b.checkoutAllowed(s) && result.Subscription == nil {
		for _, interval := range []billing.Interval{billing.Monthly, billing.Annual} {
			if offer, _, ok := b.offer(s, interval); ok {
				plans = append(plans, nativeBillingPlan{Interval: interval, Product: offer.Product, BasePlan: offer.BasePlan})
			}
		}
	}
	a.write(w, 200, nativeBillingStatus{
		Store: s, CheckoutEnabled: len(plans) != 0, AppleAttestationRequired: s == billing.Apple && b.appleAttestationEnabled(), Plans: plans, RestoreAvailable: restore,
		Pending: result.Pending, Subscription: result.Subscription, Management: result.Management, Resume: result.Resume,
	})
}

func (a *API) billingReserve(w http.ResponseWriter, r *http.Request) {
	b := a.Config.NativeBilling
	if !b.CheckoutEnabled {
		a.billingError(w, r, billing.ErrPurchaseBinding)
		return
	}
	if !a.personalNoQuery(w, r) {
		return
	}
	var body struct {
		Interval billing.Interval `json:"interval"`
	}
	if !a.decodePersonal(w, r, &body, 512) {
		return
	}
	a.Store.AccountWork.RLock()
	defer a.Store.AccountWork.RUnlock()
	id, s, err := a.billingActor(r)
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	if !b.checkoutAllowed(s) {
		a.billingError(w, r, billing.ErrPurchaseBinding)
		return
	}
	if s == billing.Apple && b.appleAttestationEnabled() {
		if _, err := b.AppleAttestation.Registry.EnrolledInstance(r.Context(), id, info(r).Family); err != nil {
			a.billingError(w, r, err)
			return
		}
	}
	offer, _, ok := b.offer(s, body.Interval)
	if !ok {
		a.billingError(w, r, billing.ErrPurchaseBinding)
		return
	}
	attempt, err := b.Journal.ReserveNative(r.Context(), id, info(r).Family, b.Environment, s, offer.Interval, time.Now())
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	a.write(w, http.StatusCreated, map[string]string{"id": attempt.ID, "binding": attempt.Binding,
		"store": string(s), "product": offer.Product, "basePlan": offer.BasePlan, "interval": string(offer.Interval)})
}

func (a *API) billingAttempt(r *http.Request) (string, billing.Attempt, error) {
	id, s, err := a.billingActor(r)
	if err != nil {
		return "", billing.Attempt{}, err
	}
	b := a.Config.NativeBilling
	attempt, err := b.Journal.NativeAttempt(r.Context(), id, info(r).Family, r.PathValue("checkout"), b.Environment, s)
	return id, attempt, err
}

func (a *API) billingReleaseUnused(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	if r.ContentLength != 0 {
		a.fail(w, r, 400, "invalid_input", "This request does not accept a body.")
		return
	}
	a.Store.AccountWork.RLock()
	defer a.Store.AccountWork.RUnlock()
	_, attempt, err := a.billingAttempt(r)
	if err == nil {
		// This route never accepts a terminal store decision from the app.
		err = a.Config.NativeBilling.Journal.ReleaseUnused(r.Context(), attempt)
	}
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) billingLaunch(w http.ResponseWriter, r *http.Request) {
	b := a.Config.NativeBilling
	if !b.CheckoutEnabled {
		a.billingError(w, r, billing.ErrPurchaseBinding)
		return
	}
	if !a.personalNoQuery(w, r) {
		return
	}
	if b.appleAttestationEnabled() && info(r).Platform == "ios" {
		a.billingAppleAuthorizedLaunch(w, r)
		return
	}
	if r.ContentLength != 0 {
		a.fail(w, r, 400, "invalid_input", "This request does not accept a body.")
		return
	}
	a.Store.AccountWork.RLock()
	defer a.Store.AccountWork.RUnlock()
	_, attempt, err := a.billingAttempt(r)
	if err == nil {
		if !b.checkoutAllowed(attempt.Store) {
			err = billing.ErrPurchaseBinding
		} else {
			err = b.Journal.Launch(r.Context(), attempt, time.Now())
		}
	}
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) billingVerify(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	var body struct {
		Receipt string `json:"receipt"`
	}
	if !a.decodePersonal(w, r, &body, 20<<10) {
		return
	}
	id, attempt, err := a.billingAttempt(r)
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	if !a.throttle(w, r, "billing-verify:"+id, 12) {
		return
	}
	b := a.Config.NativeBilling
	offer, provider, ok := b.offer(attempt.Store, attempt.Interval)
	if !ok {
		a.billingError(w, r, billing.ErrPurchaseBinding)
		return
	}
	fence := func(commit func() error) error {
		a.Store.AccountWork.RLock()
		defer a.Store.AccountWork.RUnlock()
		current, again, e := a.billingAttempt(r)
		if e != nil {
			return e
		}
		if current != id || again != attempt {
			return billing.ErrPurchaseBinding
		}
		return commit()
	}
	var result billing.CheckoutResult
	if attempt.Store == billing.Play {
		result, err = b.Play.ReconcileCheckout(r.Context(), id, attempt, offer, body.Receipt, fence)
	} else {
		result, err = b.Journal.ReconcileCheckoutWithFence(r.Context(), id, attempt, offer, body.Receipt, provider, fence)
	}
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	a.write(w, http.StatusOK, map[string]bool{"completeStoreTransaction": result.CompleteStoreTransaction})
}

func (a *API) billingRestore(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	var body struct {
		Receipt string `json:"receipt"`
	}
	if !a.decodePersonal(w, r, &body, 20<<10) {
		return
	}
	id, store, err := a.billingActor(r)
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	b := a.Config.NativeBilling
	provider, supported := b.Providers[store].(billing.RestoreProvider)
	if store == billing.Play {
		supported = b.hasPlay()
	}
	if !supported {
		a.billingError(w, r, billing.ErrPurchaseBinding)
		return
	}
	if !a.throttle(w, r, "billing-verify:"+id, 12) {
		return
	}
	fence := func(commit func() error) error {
		a.Store.AccountWork.RLock()
		defer a.Store.AccountWork.RUnlock()
		current, currentStore, e := a.billingActor(r)
		if e != nil {
			return e
		}
		if current != id || currentStore != store {
			return billing.ErrPurchaseBinding
		}
		return commit()
	}
	var result billing.CheckoutResult
	if store == billing.Play {
		result, err = b.Play.Restore(r.Context(), id, body.Receipt, fence)
	} else {
		result, err = b.Journal.RestoreWithFence(r.Context(), id, store, b.Environment, b.Offers, body.Receipt, provider, fence)
	}
	if err != nil {
		a.billingError(w, r, err)
		return
	}
	a.write(w, http.StatusOK, map[string]bool{"completeStoreTransaction": result.CompleteStoreTransaction})
}
