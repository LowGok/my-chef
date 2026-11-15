package billing

import (
	"context"
	"errors"
	"testing"
	"time"
)

type pollingVerifier struct {
	restoreProvider
	calls int
	err   error
}

func (f *pollingVerifier) VerifyExisting(ctx context.Context, receipt string) (ProviderProof, error) {
	f.calls++
	if f.err != nil {
		return ProviderProof{}, f.err
	}
	return f.restoreProvider.VerifyExisting(ctx, receipt)
}

func TestAppleReconciliationRecoversAnUnfinishedCheckoutAfterRestart(t *testing.T) {
	db, j, now := fixture(t)
	ctx := t.Context()
	attempt, err := j.Reserve(ctx, owner, Sandbox, Apple, Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	item := AppleNotice{Environment: Sandbox, ID: "99887766-5544-4321-8123-4567890abcde", Binding: attempt.Binding, Transaction: "123456"}
	if err := j.EnqueueAppleNotice(ctx, item, now); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("notice for a reservation that never launched was admitted", err)
	}
	if err := j.Launch(ctx, attempt, now); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := j.EnqueueAppleNotice(ctx, item, now); err != nil {
			t.Fatal(err)
		}
	}
	conflict := item
	conflict.Transaction = "567890"
	if err := j.EnqueueAppleNotice(ctx, conflict, now); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("a duplicate event could be redirected to another transaction", err)
	}
	p := ProviderProof{App: "preview", Product: "monthly", Region: "BEL", Period: VerifiedPeriod{Store: Apple, Environment: Sandbox,
		Binding: attempt.Binding, Subscription: "123456", Transaction: "123456", Interval: Monthly,
		StartsAt: now, PaidThrough: now.Add(time.Hour), ObservedAt: now}}
	provider := &pollingVerifier{restoreProvider: restoreProvider{proof: p}, err: ErrStoreUnavailable}
	clock := now
	fences := 0
	runner := AppleReconciler{Journal: j, Environment: Sandbox, Provider: provider, Now: func() time.Time { return clock },
		Offers: []Offer{{Store: Apple, Environment: Sandbox, App: "preview", Product: "monthly", Interval: Monthly}},
		WithLiveOwner: func(_ context.Context, who string, commit func() error) error {
			if who != owner {
				t.Fatal("worker did not use the original owner")
			}
			fences++
			return commit()
		}}
	if batch, err := runner.RunBatch(ctx); err != nil || batch.Processed != 1 || batch.Failed != 0 || fences != 0 || provider.calls != 1 {
		t.Fatalf("failed provider call wasn't durably delayed: %+v %v", batch, err)
	}
	runner.Journal = NewJournal(db)
	provider.err = nil
	if batch, err := runner.RunBatch(ctx); err != nil || batch.Processed != 0 || provider.calls != 1 {
		t.Fatal("restart lost the retry delay", batch, err)
	}
	clock = now.Add(time.Minute)
	// Simulate a crash after a worker took the job but before the provider replied.
	lease, err := runner.Journal.claimAppleNotice(ctx, Sandbox, clock)
	if err != nil || lease.ID != item.ID {
		t.Fatal("failed to take the pending job", err)
	}
	if batch, err := runner.RunBatch(ctx); err != nil || batch.Processed != 0 || provider.calls != 1 {
		t.Fatal("second worker took the live lease", batch, err)
	}
	clock = clock.Add(time.Minute)
	if err := runner.reconcileNotice(ctx, lease, runner.Now); !errors.Is(err, ErrUnresolved) {
		t.Fatal("a late provider reply outlived its lease", err)
	}
	if period, err := j.Active(ctx, owner, Sandbox, clock); err != nil || period != nil {
		t.Fatal("an expired lease granted Premium", err)
	}
	if batch, err := runner.RunBatch(ctx); err != nil || batch.Processed != 1 || batch.Failed != 0 || fences != 2 {
		t.Fatal("work abandoned by the crashed process was lost", batch, err)
	}
	if period, err := j.Active(ctx, owner, Sandbox, clock); err != nil || period == nil || period.Transaction != item.Transaction {
		t.Fatal("missed first purchase was not recovered", err)
	}
	if err := j.EnqueueAppleNotice(ctx, item, clock); err != nil {
		t.Fatal(err)
	}
	if batch, err := runner.RunBatch(ctx); err != nil || batch.Processed != 0 || provider.calls != 3 {
		t.Fatal("provider duplicate requeued settled work", batch, err)
	}
}

func TestAppleReconciliationExhaustionOnlyReportsCounts(t *testing.T) {
	_, j, now := fixture(t)
	ctx := t.Context()
	a, err := j.Reserve(ctx, owner, Production, Apple, Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Launch(ctx, a, now); err != nil {
		t.Fatal(err)
	}
	if err := j.EnqueueAppleNotice(ctx, AppleNotice{Environment: Production, ID: "99887766-5544-4321-8123-4567890abcde", Binding: a.Binding, Transaction: "789"}, now); err != nil {
		t.Fatal(err)
	}
	provider := &pollingVerifier{err: errors.New("provider diagnostics must not leave the worker")}
	clock := now
	runner := AppleReconciler{Journal: j, Environment: Sandbox, Provider: provider, Now: func() time.Time { return clock },
		Offers: []Offer{{Store: Apple, Environment: Production, App: "preview", Product: "monthly", Interval: Monthly}},
		WithLiveOwner: func(_ context.Context, _ string, _ func() error) error {
			t.Fatal("unverified provider result reached the write fence")
			return nil
		}}
	if batch, err := runner.RunBatch(ctx); err != nil || batch.Processed != 0 || provider.calls != 0 {
		t.Fatal("sandbox worker processed production work", batch, err)
	}
	runner.Environment = Production
	for i := range 5 {
		wantFailed := 0
		if i == 4 {
			wantFailed = 1
		}
		batch, err := runner.RunBatch(ctx)
		if err != nil || batch.Processed != 1 || batch.Failed != wantFailed {
			t.Fatalf("unexpected retry %d: %+v %v", i, batch, err)
		}
		clock = clock.Add(15 * time.Minute)
	}
	if batch, err := runner.RunBatch(ctx); err != nil || batch.Processed != 0 || batch.Failed != 1 || provider.calls != 5 {
		t.Fatal("exhausted work kept running or was not reported after restart", batch, err)
	}
}
