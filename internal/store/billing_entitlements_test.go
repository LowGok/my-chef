package store

import (
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/billing"
)

func TestVerifiedNativePurchaseUsesExistingEntitlementReader(t *testing.T) {
	s, actor, other, _, now := personalFixture(t)
	ctx := t.Context()
	external, err := s.InitializeLifecycleJournal(ctx, s.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = external.Close() })
	journal, err := external.Billing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	incarnation := entitlementIdentity(t, s, actor.UserID)
	attempt, err := journal.Reserve(ctx, incarnation, billing.Sandbox, billing.Apple, billing.Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Launch(ctx, attempt, now); err != nil {
		t.Fatal(err)
	}
	ends := now.Add(time.Hour)
	if err := journal.Fulfil(ctx, billing.VerifiedPeriod{
		Store: billing.Apple, Environment: billing.Sandbox, Binding: attempt.Binding,
		Subscription: "synthetic-lineage", Transaction: "synthetic-order", Interval: billing.Monthly,
		StartsAt: now, PaidThrough: ends, ObservedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	at := now
	reader := NativeBillingEntitlementReader(journal, billing.Sandbox, EntitlementReader(FixtureEntitlementLease{}, nil), func() time.Time { return at })
	for _, row := range []struct {
		user    int64
		at      time.Time
		premium bool
	}{
		{actor.UserID, now, true},
		{other.UserID, now, false},
		{actor.UserID, ends.Add(-time.Nanosecond), true},
		{actor.UserID, ends, false},
	} {
		at = row.at
		got, err := reader(ctx, s.DB, row.user, incarnation)
		if err != nil || got.Premium != row.premium {
			t.Fatalf("read at %v for owner=%t: got %+v, %v", row.at, row.user == actor.UserID, got, err)
		}
	}
	at = now
	prod := NativeBillingEntitlementReader(journal, billing.Production, EntitlementReader(FixtureEntitlementLease{}, nil), func() time.Time { return at })
	if got, err := prod(ctx, s.DB, actor.UserID, incarnation); err != nil || got.Premium {
		t.Fatal("Sandbox purchase granted production Premium", err)
	}
}
