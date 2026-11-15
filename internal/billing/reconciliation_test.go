package billing

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

type fakeProvider struct {
	proof ProviderProof
	err   error
}

func (f fakeProvider) Verify(context.Context, string) (ProviderProof, error) {
	return f.proof, f.err
}

type restoreProvider struct {
	proof    ProviderProof
	checkErr error
}

func (f restoreProvider) Verify(context.Context, string) (ProviderProof, error) {
	return f.proof, f.checkErr
}

func (f restoreProvider) VerifyExisting(context.Context, string) (ProviderProof, error) {
	return f.proof, nil
}

func TestRestoreFindsMissedPurchaseAndOnlyRenewsAnOwnedSubscription(t *testing.T) {
	db, j, now := fixture(t)
	ctx := t.Context()
	attempt, err := j.Reserve(ctx, owner, Sandbox, Apple, Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Launch(ctx, attempt, now); err != nil {
		t.Fatal(err)
	}
	offers := []Offer{{Store: Apple, Environment: Sandbox, App: "preview", Product: "month", Interval: Monthly},
		{Store: Apple, Environment: Sandbox, App: "preview", Product: "year", Interval: Annual}}
	first := ProviderProof{App: "preview", Product: "month", Region: "BEL", Period: VerifiedPeriod{Store: Apple,
		Environment: Sandbox, Binding: attempt.Binding, Subscription: "original", Transaction: "first", Interval: Monthly,
		StartsAt: now, PaidThrough: now.Add(time.Hour), ObservedAt: now}}
	restore := func(who string, p ProviderProof, initialErr error, fence func(func() error) error) (CheckoutResult, error) {
		return j.RestoreWithFence(ctx, who, Apple, Sandbox, offers, "synthetic-reference", restoreProvider{proof: p, checkErr: initialErr}, fence)
	}
	other := "1234567890abcdef1234567890abcdec"
	if _, err := restore(other, first, nil, nil); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("reopened purchase belonged to a different account", err)
	}
	foreign := first
	foreign.Region = "FRA"
	if _, err := restore(owner, foreign, nil, nil); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("first purchase bypassed the existing Belgian offer rule", err)
	}
	if _, err := restore(owner, first, ErrPurchaseBinding, nil); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("renewal data was accepted as proof of initial sale terms", err)
	}
	if res, err := restore(owner, first, nil, func(func() error) error { return context.Canceled }); !errors.Is(err, context.Canceled) || res.CompleteStoreTransaction {
		t.Fatal("failed session fence authorized completion", err)
	}
	if res, err := restore(owner, first, nil, nil); err != nil || !res.CompleteStoreTransaction {
		t.Fatal("lost callback could not be recovered", err)
	}
	renewed := first
	renewed.Product, renewed.Region = "year", "FRA"
	renewed.Period.Transaction, renewed.Period.Interval = "next", Annual
	renewed.Period.StartsAt = first.Period.PaidThrough
	renewed.Period.PaidThrough = renewed.Period.StartsAt.Add(2 * time.Hour)
	renewed.Period.ObservedAt = renewed.Period.StartsAt
	if _, err := restore(other, renewed, nil, nil); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("renewal attached to a different account", err)
	}
	overlap := renewed
	overlap.Period.StartsAt = first.Period.StartsAt.Add(time.Minute)
	if _, err := restore(owner, overlap, nil, nil); !errors.Is(err, ErrStalePurchase) {
		t.Fatal("overlapping renewal was accepted", err)
	}
	for range 2 {
		if res, err := restore(owner, renewed, ErrPurchaseBinding, nil); err != nil || !res.CompleteStoreTransaction {
			t.Fatal("rightful renewal could not be restored after storefront or interval change", err)
		}
	}
	at := renewed.Period.StartsAt.Add(time.Minute)
	if p, err := j.Active(ctx, owner, Sandbox, at); err != nil || p == nil || p.Interval != Annual {
		t.Fatal("renewal was not active at the verified boundary", err)
	}
	revoked := renewed
	revoked.Period.Revoked = true
	revoked.Period.ObservedAt = at
	if _, err := restore(owner, revoked, nil, nil); err != nil {
		t.Fatal("verified revocation was not applied", err)
	}
	if _, err := restore(owner, renewed, nil, nil); !errors.Is(err, ErrStalePurchase) {
		t.Fatal("old purchase resurrected revoked Premium", err)
	}
	if p, err := j.Active(ctx, owner, Sandbox, at); err != nil || p != nil {
		t.Fatal("Premium still active after revocation", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM billing_periods").Scan(&n); err != nil || n != 2 {
		t.Fatal("retries or rejected proofs changed the number of paid periods", err)
	}
}

func TestVerifyMatchesStoreOfferAndAccountBeforeDurableFulfilment(t *testing.T) {
	db, journal, now := fixture(t)
	ctx := t.Context()
	attempt, err := journal.Reserve(ctx, owner, Sandbox, Apple, Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Launch(ctx, attempt, now); err != nil {
		t.Fatal(err)
	}
	offer := Offer{Store: Apple, Environment: Sandbox, App: "com.example.preview", Product: "premium-monthly", Interval: Monthly}
	proof := ProviderProof{App: offer.App, Product: offer.Product, Region: "BEL",
		Period: VerifiedPeriod{Store: Apple, Environment: Sandbox, Binding: attempt.Binding, Subscription: "fixture-line",
			Transaction: "fixture-order", Interval: Monthly, StartsAt: now, PaidThrough: now.Add(time.Hour), ObservedAt: now}}
	for _, example := range []struct {
		name  string
		alter func(*ProviderProof)
	}{
		{"wrong app", func(p *ProviderProof) { p.App = "com.other.preview" }},
		{"wrong SKU", func(p *ProviderProof) { p.Product = "premium-other" }},
		{"non-Belgian", func(p *ProviderProof) { p.Region = "FRA" }},
		{"trial", func(p *ProviderProof) { p.Trial = true }},
		{"wrong binding", func(p *ProviderProof) { p.Period.Binding = "another-account" }},
		{"wrong environment", func(p *ProviderProof) { p.Period.Environment = Production }},
	} {
		t.Run(example.name, func(t *testing.T) {
			bad := proof
			example.alter(&bad)
			if _, err := journal.ReconcileCheckout(t.Context(), owner, attempt, offer, "fixture-receipt", fakeProvider{proof: bad}); !errors.Is(err, ErrPurchaseBinding) {
				t.Fatalf("proof was not rejected: %v", err)
			}
		})
	}
	other := "1234567890abcdef1234567890abcdec"
	if _, err := journal.ReconcileCheckout(ctx, other, attempt, offer, "fixture-receipt", fakeProvider{proof: proof}); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatalf("wrong account was allowed to claim the proof: %v", err)
	}
	if _, err := journal.ReconcileCheckout(ctx, owner, attempt, offer, "fixture-receipt", fakeProvider{err: context.DeadlineExceeded}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost provider reply unexpectedly fulfilled: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM billing_periods").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid receipts wrote a paid period", err)
	}
	for range 2 {
		result, err := journal.ReconcileCheckout(ctx, owner, attempt, offer, "fixture-receipt", fakeProvider{proof: proof})
		if err != nil || !result.CompleteStoreTransaction {
			t.Fatal("expected verified durable completion", err)
		}
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM billing_periods").Scan(&count); err != nil || count != 1 {
		t.Fatal("verification did not converge on one paid period", err)
	}
}

func TestReconciliationNeverReturnsCompletionAfterFailedWrite(t *testing.T) {
	db, journal, now := fixture(t)
	ctx := t.Context()
	attempt, err := journal.Reserve(ctx, owner, Sandbox, Play, Annual, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Launch(ctx, attempt, now); err != nil {
		t.Fatal(err)
	}
	offer := Offer{Store: Play, Environment: Sandbox, App: "com.example.preview", Product: "premium", BasePlan: "annual", Interval: Annual}
	p := ProviderProof{App: offer.App, Product: offer.Product, BasePlan: offer.BasePlan, Region: "BE",
		Period: VerifiedPeriod{Store: Play, Environment: Sandbox, Binding: attempt.Binding, Subscription: "fixture-line", Transaction: "fixture-order", Interval: Annual, StartsAt: now, PaidThrough: now.Add(time.Hour), ObservedAt: now}}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER unavailable BEFORE INSERT ON billing_periods BEGIN SELECT RAISE(ABORT,'fixture unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := journal.ReconcileCheckout(ctx, owner, attempt, offer, "fixture-proof", fakeProvider{proof: p})
	if err == nil || result.CompleteStoreTransaction {
		t.Fatal("storage failure authorized store acknowledgement")
	}
	var status string
	if err := db.QueryRowContext(ctx, "SELECT state FROM billing_attempts WHERE id=?", attempt.ID).Scan(&status); errors.Is(err, sql.ErrNoRows) || err != nil || status != "launched" {
		t.Fatalf("attempt was not kept pending: %q %v", status, err)
	}
}
