package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/billing"
)

type restoredPurchaseVerifier struct{ proof billing.ProviderProof }

func (v restoredPurchaseVerifier) Verify(context.Context, string) (billing.ProviderProof, error) {
	return v.proof, nil
}
func (v restoredPurchaseVerifier) VerifyExisting(context.Context, string) (billing.ProviderProof, error) {
	return v.proof, nil
}

type delayedRestoreVerifier struct {
	restoredPurchaseVerifier
	started, release chan struct{}
}

func (v delayedRestoreVerifier) VerifyExisting(ctx context.Context, _ string) (billing.ProviderProof, error) {
	close(v.started)
	select {
	case <-v.release:
		return v.proof, nil
	case <-ctx.Done():
		return billing.ProviderProof{}, ctx.Err()
	}
}

func TestRestoredBillingRequiresFreshProofForTheOriginalIncarnation(t *testing.T) {
	s, j, actor := accountDeletionFixture(t)
	ctx := t.Context()
	now := time.Now().UTC()
	inc := entitlementIdentity(t, s, actor.UserID)
	if err := s.DB.Exec(`UPDATE users SET email_verified_at=? WHERE user_id=?`, now.Unix(), actor.UserID).Error; err != nil {
		t.Fatal(err)
	}
	// The purchase happens after this backup. Restore must preserve the
	// independently recorded sale once the original owner re-verifies it.
	backup := filepath.Join(t.TempDir(), "before-sale.db")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	before, err := j.Billing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a, err := before.Reserve(ctx, inc, billing.Sandbox, billing.Apple, billing.Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := before.Launch(ctx, a, now); err != nil {
		t.Fatal(err)
	}
	period := billing.VerifiedPeriod{Store: billing.Apple, Environment: billing.Sandbox, Binding: a.Binding, Subscription: "92201", Transaction: "92201",
		Interval: billing.Monthly, StartsAt: now, PaidThrough: now.Add(time.Hour), ObservedAt: now}
	if err := before.Fulfil(ctx, period); err != nil {
		t.Fatal(err)
	}
	offers := []billing.Offer{{Store: billing.Apple, Environment: billing.Sandbox, App: "preview", Product: "monthly", Interval: billing.Monthly}}
	proof := restoredPurchaseVerifier{billing.ProviderProof{App: "preview", Product: "monthly", Region: "BEL", Period: period}}
	delayed := delayedRestoreVerifier{restoredPurchaseVerifier: proof, started: make(chan struct{}), release: make(chan struct{})}
	answer := make(chan error, 1)
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	go func() {
		_, err := before.RestoreWithFence(lookupCtx, inc, billing.Apple, billing.Sandbox, offers, period.Transaction, delayed, nil)
		answer <- err
	}()
	select {
	case <-delayed.started:
	case <-lookupCtx.Done():
		t.Fatal("old worker did not reach provider lookup")
	}
	dest := filepath.Join(t.TempDir(), "restored.db")
	if err := Restore(ctx, backup, dest, AccountRestoreOptions{JournalPath: j.Path}); err != nil {
		t.Fatal(err)
	}
	close(delayed.release)
	select {
	case err := <-answer:
		if !errors.Is(err, billing.ErrUnresolved) {
			t.Fatal("a genuinely in-flight provider response committed after restore", err)
		}
	case <-lookupCtx.Done():
		t.Fatal("in-flight verifier did not finish")
	}
	if err := before.Fulfil(ctx, period); err == nil {
		t.Fatal("an in-flight worker from before restore retained authority")
	}
	r, err := Open(dest, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	newPair, err := r.OpenLifecycleJournal(ctx, j.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = newPair.Close() })
	after, err := newPair.Billing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reader := NativeBillingEntitlementReader(after, billing.Sandbox, nil, func() time.Time { return now })
	check := func(premium bool) {
		t.Helper()
		v, err := reader(ctx, r.DB, actor.UserID, inc)
		if err != nil || v.Premium != premium {
			t.Fatal("unexpected restored entitlement", v, err)
		}
	}
	check(false)
	var sessions int
	if err := r.SQL.QueryRowContext(ctx, "SELECT count(*) FROM sessions").Scan(&sessions); err != nil || sessions != 0 {
		t.Fatal("restore revived old signed-in sessions", err)
	}
	if _, err := after.Reserve(ctx, inc, billing.Sandbox, billing.Play, billing.Monthly, now); !errors.Is(err, billing.ErrUnresolved) {
		t.Fatal("pending recovery could start another store purchase", err)
	}
	other := entitlementIdentity(t, r, 2)
	if _, err := after.RestoreWithFence(ctx, other, billing.Apple, billing.Sandbox, offers, period.Transaction, proof, nil); !errors.Is(err, billing.ErrPurchaseBinding) {
		t.Fatal("another account claimed the original", err)
	}
	got, err := after.RestoreWithFence(ctx, inc, billing.Apple, billing.Sandbox, offers, period.Transaction, proof, func(commit func() error) error {
		return newPair.WithLiveBillingOwner(ctx, inc, commit)
	})
	if err != nil || !got.CompleteStoreTransaction {
		t.Fatal("fresh lookup did not rebuild the original purchase", err)
	}
	check(true)
	if err := before.Fulfil(ctx, period); err == nil {
		t.Fatal("old generation wrote after verification")
	}
}

func TestRestoredBillingKeepsPurchaseWhenSnapshotPredatesItsAccount(t *testing.T) {
	s, j, _ := accountDeletionFixture(t)
	ctx := t.Context()
	now := time.Now().UTC()
	backup := filepath.Join(t.TempDir(), "before-account.db")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	create := func(db *Store) string {
		t.Helper()
		if err := db.DB.Exec(`INSERT INTO users(user_id,firstname,lastname,email,email_key,password,username,email_verified_at)
 VALUES(3,'Fixture','Owner','later@example.test','later@example.test','fixture-hash','later-owner',?)`, now.Unix()).Error; err != nil {
			t.Fatal(err)
		}
		return entitlementIdentity(t, db, 3)
	}
	old := create(s)
	b, err := j.Billing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a, err := b.Reserve(ctx, old, billing.Sandbox, billing.Apple, billing.Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Launch(ctx, a, now); err != nil {
		t.Fatal(err)
	}
	p := billing.VerifiedPeriod{Store: billing.Apple, Environment: billing.Sandbox, Interval: billing.Monthly,
		Binding: a.Binding, Subscription: "92202", Transaction: "92202", StartsAt: now, PaidThrough: now.Add(time.Hour), ObservedAt: now}
	if err := b.Fulfil(ctx, p); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "restored-before-account.db")
	if err := Restore(ctx, backup, dest, AccountRestoreOptions{JournalPath: j.Path}); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dest, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	paired, err := r.OpenLifecycleJournal(ctx, j.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = paired.Close() })
	after, err := paired.Billing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fresh := create(r)
	if fresh == old {
		t.Fatal("fresh signup reused an incarnation")
	}
	if err := paired.WithLiveBillingOwner(ctx, old, func() error { t.Fatal("worker could grant for an absent account"); return nil }); !errors.Is(err, billing.ErrPurchaseBinding) {
		t.Fatal("missing incarnation was not fenced", err)
	}
	proof := restoredPurchaseVerifier{billing.ProviderProof{App: "preview", Product: "monthly", Region: "BEL", Period: p}}
	offers := []billing.Offer{{Store: billing.Apple, Environment: billing.Sandbox, App: "preview", Product: "monthly", Interval: billing.Monthly}}
	if _, err := after.RestoreWithFence(ctx, fresh, billing.Apple, billing.Sandbox, offers, p.Transaction, proof, nil); !errors.Is(err, billing.ErrPurchaseBinding) {
		t.Fatal("same email could claim the previous incarnation's purchase", err)
	}
	var count int
	if err := paired.store.SQL.QueryRowContext(ctx, `SELECT count(*) FROM billing_subscriptions WHERE incarnation=? AND subscription=?`, old, p.Subscription).Scan(&count); err != nil || count != 1 {
		t.Fatal("legitimate purchase was discarded instead of held for recovery", err)
	}
}
