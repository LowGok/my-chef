package billing

import (
	"testing"
	"time"
)

func TestNativeOverviewUsesVerifiedRenewalWithoutChangingPaidThrough(t *testing.T) {
	db, j, now := fixture(t)
	ctx := t.Context()
	a, err := j.Reserve(ctx, owner, Sandbox, Play, Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Launch(ctx, a, now); err != nil {
		t.Fatal(err)
	}
	p := VerifiedPeriod{Store: Play, Environment: Sandbox, Binding: a.Binding,
		Subscription: "lineage", Transaction: "order", Interval: Monthly,
		StartsAt: now, PaidThrough: now.Add(time.Hour), ObservedAt: now}
	check := func(at time.Time, want RenewalState) {
		t.Helper()
		view, err := j.NativeOverview(ctx, owner, owner, Sandbox, Apple, at)
		if err != nil || view.Subscription == nil || view.Subscription.Renewal != want ||
			!view.Subscription.PaidThrough.Equal(p.PaidThrough) {
			t.Fatalf("subscription overview = %+v, error = %v; wanted renewal %q and the original funded date", view.Subscription, err, want)
		}
	}
	if err := j.Fulfil(ctx, p); err != nil {
		t.Fatal(err)
	}
	check(now, "")
	p.Renewal = &RenewalObservation{State: RenewalRenews, ObservedAt: now.Add(time.Minute)}
	if err := j.Fulfil(ctx, p); err != nil {
		t.Fatal(err)
	}
	check(now.Add(time.Minute), RenewalRenews)

	p.Renewal = &RenewalObservation{State: RenewalEnds, ObservedAt: now.Add(2 * time.Minute)}
	if err := j.Fulfil(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Renewal = &RenewalObservation{State: RenewalRenews, ObservedAt: now.Add(time.Minute)}
	if err := j.Fulfil(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Renewal = nil
	if err := j.Fulfil(ctx, p); err != nil {
		t.Fatal(err)
	}
	check(now.Add(3*time.Minute), RenewalEnds)

	j, err = StartRuntime(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Fulfil(ctx, p); err != nil {
		t.Fatal(err)
	}
	// A fresh transaction verifies the paid period, not the old runtime's
	// renewal state. The provider must also reverify that observation.
	check(now.Add(3*time.Minute), "")
	p.Renewal = &RenewalObservation{State: RenewalEnds, ObservedAt: now.Add(2 * time.Minute)}
	if err := j.Fulfil(ctx, p); err != nil {
		t.Fatal(err)
	}
	check(now.Add(3*time.Minute), RenewalEnds)
	view, err := j.NativeOverview(ctx, owner, owner, Sandbox, Apple, p.PaidThrough)
	if err != nil || view.Subscription != nil || view.Management == nil || view.Management.State != "ended" {
		t.Fatalf("an ending subscription still granted access at the cutoff: %+v, %v", view, err)
	}
}
