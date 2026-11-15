package billing

import (
	"context"
	"database/sql"
	"errors"
)

// Offer is selected from server configuration for the exact app and store.
// Product and base plan are checked independently on Play.
type Offer struct {
	Store                  Store
	Environment            Environment
	App, Product, BasePlan string
	Interval               Interval
}

// ProviderProof holds only facts returned by the configured store verifier.
type ProviderProof struct {
	App, Product, BasePlan, Region string
	Trial                          bool
	Period                         VerifiedPeriod
}

// Provider verifies an opaque client receipt against the store. The service
// must select the provider itself; client input cannot select an endpoint.
type Provider interface {
	Verify(context.Context, string) (ProviderProof, error)
}

// RestoreProvider verifies existing paid periods without enforcing a new
// checkout's price or storefront. Initial recovery still calls Verify.
type RestoreProvider interface {
	Provider
	VerifyExisting(context.Context, string) (ProviderProof, error)
}

type CheckoutResult struct {
	CompleteStoreTransaction bool
}

func (offer Offer) matches(proof ProviderProof) bool {
	p := proof.Period
	return validOffer(offer.Store, offer.Environment, offer.Interval) &&
		offer.App != "" && offer.Product != "" && (offer.Store != Play || offer.BasePlan != "") &&
		offer.Store == p.Store && offer.Environment == p.Environment && offer.Interval == p.Interval &&
		offer.App == proof.App && offer.Product == proof.Product && offer.BasePlan == proof.BasePlan && !proof.Trial
}

func (offer Offer) admits(attempt Attempt, proof ProviderProof) bool {
	p := proof.Period
	region := "BEL"
	if offer.Store == Play {
		region = "BE"
	}
	return offer.matches(proof) && offer.Store == attempt.Store && offer.Environment == attempt.Environment &&
		offer.Interval == attempt.Interval && region == proof.Region && !p.Revoked && p.Binding == attempt.Binding
}

// ReconcileCheckout verifies the first purchase, or retries that purchase
// after a lost response. Renewal and revocation use a separate reconciliation
// path so changing an existing subscriber's storefront cannot orphan access.
func (j *Journal) ReconcileCheckout(ctx context.Context, owner string, attempt Attempt, offer Offer, opaque string, provider Provider) (CheckoutResult, error) {
	return j.ReconcileCheckoutWithFence(ctx, owner, attempt, offer, opaque, provider, nil)
}

// ReconcileCheckoutWithFence permits the account service to recheck session
// authority and serialize the durable commit with deletion after provider I/O.
func (j *Journal) ReconcileCheckoutWithFence(ctx context.Context, owner string, attempt Attempt, offer Offer, opaque string, provider Provider, fence func(func() error) error) (CheckoutResult, error) {
	return j.reconcileCheckoutWithHook(ctx, owner, attempt, offer, opaque, provider, fence, nil)
}

type journalPeriodHook func(context.Context, *sql.Tx, VerifiedPeriod) error

func proofHook(proof VerifiedPeriod, check journalWriteCheck, persist journalPeriodHook) journalWriteCheck {
	if persist == nil {
		return check
	}
	return func(ctx context.Context, tx *sql.Tx) error {
		if check != nil {
			if err := check(ctx, tx); err != nil {
				return err
			}
		}
		return persist(ctx, tx, proof)
	}
}

func (j *Journal) reconcileCheckoutWithHook(ctx context.Context, owner string, attempt Attempt, offer Offer, opaque string, provider Provider, fence func(func() error) error, persist journalPeriodHook) (CheckoutResult, error) {
	if provider == nil || !validOwner(owner) || len(opaque) == 0 || len(opaque) > 16<<10 {
		return CheckoutResult{}, ErrPurchaseBinding
	}
	if err := j.CurrentRuntime(ctx); err != nil {
		return CheckoutResult{}, err
	}
	var saved string
	err := j.db.QueryRowContext(ctx, `SELECT incarnation FROM billing_attempts
 WHERE id=? AND binding=? AND store=? AND environment=? AND interval=? AND state IN ('launched','fulfilled')
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=billing_attempts.incarnation AND d.state!='cancelled')`,
		attempt.ID, attempt.Binding, attempt.Store, attempt.Environment, attempt.Interval).Scan(&saved)
	if errors.Is(err, sql.ErrNoRows) || err == nil && saved != owner {
		return CheckoutResult{}, ErrPurchaseBinding
	}
	if err != nil {
		return CheckoutResult{}, err
	}
	proof, err := provider.Verify(ctx, opaque)
	if err != nil {
		return CheckoutResult{}, err
	}
	if !offer.admits(attempt, proof) {
		return CheckoutResult{}, ErrPurchaseBinding
	}
	// Fulfil rechecks the immutable binding and attempt state under the write
	// lock. Only return the finish/acknowledge signal after its commit succeeds.
	return completePurchase(fence, func() error { return j.fulfil(ctx, proof.Period, owner, false, proofHook(proof.Period, nil, persist)) })
}

// RestoreWithFence recovers an unfinished first purchase using its store-signed
// binding, or refreshes an already owned lineage. It cannot create a new
// checkout or use an existing customer's price to admit an initial purchase.
func (j *Journal) RestoreWithFence(ctx context.Context, owner string, store Store, env Environment, offers []Offer, opaque string, provider RestoreProvider, fence func(func() error) error) (CheckoutResult, error) {
	return j.restoreWithLease(ctx, owner, store, env, offers, opaque, provider, fence, nil)
}

func (j *Journal) restoreWithLease(ctx context.Context, owner string, store Store, env Environment, offers []Offer, opaque string, provider RestoreProvider, fence func(func() error) error, beforeWrite journalWriteCheck) (CheckoutResult, error) {
	return j.restoreWithHooks(ctx, owner, store, env, offers, opaque, provider, fence, beforeWrite, nil)
}

func (j *Journal) restoreWithHooks(ctx context.Context, owner string, store Store, env Environment, offers []Offer, opaque string, provider RestoreProvider, fence func(func() error) error, beforeWrite journalWriteCheck, persist journalPeriodHook) (CheckoutResult, error) {
	if provider == nil || !validOwner(owner) || !validOffer(store, env, Monthly) || len(opaque) == 0 || len(opaque) > 16<<10 {
		return CheckoutResult{}, ErrPurchaseBinding
	}
	if err := j.CurrentRuntime(ctx); err != nil {
		return CheckoutResult{}, err
	}
	if blocked, err := ownerFenced(ctx, j.db, owner); err != nil {
		return CheckoutResult{}, err
	} else if blocked {
		return CheckoutResult{}, ErrPurchaseBinding
	}
	proof, err := provider.VerifyExisting(ctx, opaque)
	if err != nil {
		return CheckoutResult{}, err
	}
	p := proof.Period
	if p.Store != store || p.Environment != env || p.Binding == "" || p.Subscription == "" {
		return CheckoutResult{}, ErrPurchaseBinding
	}
	var offer Offer
	for _, candidate := range offers {
		if candidate.matches(proof) {
			offer = candidate
			break
		}
	}
	if offer.Product == "" {
		return CheckoutResult{}, ErrPurchaseBinding
	}
	var knownOwner, knownBinding string
	err = j.db.QueryRowContext(ctx, `SELECT incarnation,binding FROM billing_subscriptions
 WHERE environment=? AND store=? AND subscription=?`, env, store, p.Subscription).Scan(&knownOwner, &knownBinding)
	if err == nil {
		if knownOwner != owner || knownBinding != p.Binding {
			return CheckoutResult{}, ErrPurchaseBinding
		}
		return completePurchase(fence, func() error { return j.fulfil(ctx, p, owner, true, proofHook(p, beforeWrite, persist)) })
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CheckoutResult{}, err
	}
	attempt := Attempt{Binding: p.Binding, Store: store, Environment: env}
	err = j.db.QueryRowContext(ctx, `SELECT id,interval FROM billing_attempts
 WHERE incarnation=? AND environment=? AND store=? AND binding=? AND state='launched'`,
		owner, env, store, p.Binding).Scan(&attempt.ID, &attempt.Interval)
	if errors.Is(err, sql.ErrNoRows) {
		return CheckoutResult{}, ErrPurchaseBinding
	}
	if err != nil {
		return CheckoutResult{}, err
	}
	initial, err := provider.Verify(ctx, opaque)
	if err != nil {
		return CheckoutResult{}, err
	}
	if initial.Period.Subscription != p.Subscription || initial.Period.Transaction != p.Transaction || !offer.admits(attempt, initial) {
		return CheckoutResult{}, ErrPurchaseBinding
	}
	return completePurchase(fence, func() error {
		return j.fulfil(ctx, initial.Period, owner, false, proofHook(initial.Period, beforeWrite, persist))
	})
}

func completePurchase(fence func(func() error) error, commit func() error) (CheckoutResult, error) {
	var err error
	if fence != nil {
		err = fence(commit)
	} else {
		err = commit()
	}
	if err != nil {
		return CheckoutResult{}, err
	}
	return CheckoutResult{CompleteStoreTransaction: true}, nil
}
