package billing

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

const owner = "1234567890abcdef1234567890abcdef"

func fixture(t *testing.T) (*sql.DB, *Journal, time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "billing.db")+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE lifecycle_deletions(incarnation TEXT NOT NULL, state TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return db, NewJournal(db), time.Date(2026, 11, 14, 18, 0, 0, 0, time.UTC)
}

func TestReserveOneCheckoutAcrossStoresAndKeepLaunchedAttempts(t *testing.T) {
	db, journal, now := fixture(t)
	ctx := t.Context()
	var wg sync.WaitGroup
	type result struct {
		attempt Attempt
		err     error
	}
	r := make(chan result, 16)
	for i := range cap(r) {
		wg.Go(func() {
			store := Apple
			if i%2 == 0 {
				store = Play
			}
			attempt, err := journal.Reserve(ctx, owner, Sandbox, store, Monthly, now)
			r <- result{attempt, err}
		})
	}
	wg.Wait()
	close(r)
	var selected Attempt
	for reply := range r {
		if reply.err == nil {
			if selected.ID != "" {
				t.Fatal("two checkouts reserved")
			}
			selected = reply.attempt
		} else if !errors.Is(reply.err, ErrCheckoutBusy) {
			t.Fatal(reply.err)
		}
	}
	if selected.ID == "" || selected.Binding == "" {
		t.Fatal("missing reservation")
	}
	if err := journal.Launch(ctx, selected, now); err != nil {
		t.Fatal(err)
	}
	restarted := NewJournal(db)
	if _, err := restarted.Reserve(ctx, owner, Sandbox, Play, Annual, now.Add(24*time.Hour)); !errors.Is(err, ErrCheckoutBusy) {
		t.Fatalf("unresolved checkout let another store open: %v", err)
	}
	if err := restarted.ReleaseUnused(ctx, selected); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("ambiguous outcome cleared checkout: %v", err)
	}

}

func TestPaidPeriodIsOwnerBoundExclusiveAndCannotResurrect(t *testing.T) {
	_, journal, now := fixture(t)
	ctx := t.Context()
	attempt, err := journal.Reserve(ctx, owner, Sandbox, Apple, Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Launch(ctx, attempt, now); err != nil {
		t.Fatal(err)
	}
	period := VerifiedPeriod{Store: Apple, Environment: Sandbox, Binding: attempt.Binding,
		Subscription: "fake-lineage", Transaction: "fake-purchase", Interval: Monthly,
		StartsAt: now, PaidThrough: now.Add(time.Hour), ObservedAt: now}
	for range 2 {
		if err := journal.Fulfil(ctx, period); err != nil {
			t.Fatal(err)
		}
	}
	wrong := period
	wrong.Store = Play
	if err := journal.Fulfil(ctx, wrong); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatalf("foreign proof: %v", err)
	}
	if _, err := journal.Reserve(ctx, owner, Sandbox, Play, Annual, now); !errors.Is(err, ErrAlreadyPremium) {
		t.Fatalf("overlapping checkout: %v", err)
	}
	for _, scenario := range []struct {
		at time.Time
		ok bool
	}{
		{now.Add(-time.Nanosecond), false}, {now, true}, {period.PaidThrough.Add(-time.Nanosecond), true}, {period.PaidThrough, false},
	} {
		got, err := journal.Active(ctx, owner, Sandbox, scenario.at)
		if err != nil || (got != nil) != scenario.ok {
			t.Fatalf("unexpected entitlement at %s: %v %v", scenario.at, got != nil, err)
		}
	}
	if got, err := journal.Active(ctx, owner, Production, now); err != nil || got != nil {
		t.Fatal("sandbox granted production Premium", err)
	}
	revoked := period
	revoked.ObservedAt, revoked.Revoked = now.Add(time.Minute), true
	if err := journal.Fulfil(ctx, revoked); err != nil {
		t.Fatal(err)
	}
	if err := journal.Fulfil(ctx, period); !errors.Is(err, ErrStalePurchase) {
		t.Fatalf("old event resurrected period: %v", err)
	}
	if got, err := journal.Active(ctx, owner, Sandbox, revoked.ObservedAt); err != nil || got != nil {
		t.Fatal("revoked period retained Premium", err)
	}
}

func TestUnusedReservationCanExpireWithoutAllowingOldLaunch(t *testing.T) {
	_, journal, now := fixture(t)
	ctx := context.Background()
	id, err := journal.Reserve(ctx, owner, Sandbox, Apple, Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	after := now.Add(ReservationLifetime)
	if _, err := journal.Reserve(ctx, owner, Sandbox, Play, Annual, after); err != nil {
		t.Fatal(err)
	}
	if err := journal.Launch(ctx, id, after); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("expired attempt was launched: %v", err)
	}
}

func TestBillingManagementSurvivesExpiryAndDoesNotReuseUnverifiedDates(t *testing.T) {
	db, j, now := fixture(t)
	ctx := t.Context()
	const family = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const other = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	attempt, err := j.ReserveNative(ctx, owner, family, Sandbox, Play, Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Launch(ctx, attempt, now); err != nil {
		t.Fatal(err)
	}
	pending, err := j.NativeOverview(ctx, owner, family, Sandbox, Apple, now)
	if err != nil || pending.Subscription != nil || pending.Resume != nil || pending.Management == nil || pending.Management.Store != Play ||
		pending.Management.State != "pending_verification" || pending.Management.EndedAt != nil {
		t.Fatalf("pending purchase must point to its own store without a date: %+v, %v", pending, err)
	}
	end := now.Add(time.Hour)
	period := VerifiedPeriod{Store: Play, Environment: Sandbox, Binding: attempt.Binding, Subscription: "offline-lineage", Transaction: "offline-order",
		Interval: Monthly, StartsAt: now, PaidThrough: end, ObservedAt: now}
	if err := j.Fulfil(ctx, period); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Reserve(ctx, owner, Sandbox, Apple, Annual, now.Add(-time.Second)); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("a verified future paid period allowed checkout before its start: %v", err)
	}
	upcoming, err := j.NativeOverview(ctx, owner, family, Sandbox, Apple, now.Add(-time.Second))
	if err != nil || !upcoming.Pending || upcoming.Subscription != nil {
		t.Fatalf("an upcoming paid period was presented as active or allowed checkout: %+v, %v", upcoming, err)
	}
	active, err := j.NativeOverview(ctx, owner, family, Sandbox, Apple, now)
	if err != nil || active.Subscription == nil || active.Management != nil || active.Subscription.Store != Play {
		t.Fatalf("active purchase did not retain its seller: %+v, %v", active, err)
	}
	ended, err := j.NativeOverview(ctx, owner, family, Sandbox, Apple, end)
	if err != nil || ended.Subscription != nil || ended.Management == nil || ended.Management.Store != Play ||
		ended.Management.State != "ended" || ended.Management.EndedAt == nil || !ended.Management.EndedAt.Equal(end) {
		t.Fatalf("verified expiry lost the actual selling store and end date: %+v, %v", ended, err)
	}
	current, err := StartRuntime(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := current.NativeOverview(ctx, owner, family, Sandbox, Apple, end)
	if err != nil || stale.Subscription != nil || !stale.Pending || stale.Management == nil || stale.Management.Store != Play ||
		stale.Management.State != "pending_verification" || stale.Management.EndedAt != nil {
		t.Fatalf("restored runtime exposed an unverified end date or lost the store: %+v, %v", stale, err)
	}
	period.ObservedAt = end
	period.Revoked = true
	if err := current.Fulfil(ctx, period); err != nil {
		t.Fatal(err)
	}
	revoked, err := current.NativeOverview(ctx, owner, family, Sandbox, Apple, end)
	if err != nil || revoked.Subscription != nil || revoked.Management == nil || revoked.Management.Store != Play ||
		revoked.Management.State != "revoked" || revoked.Management.EndedAt != nil {
		t.Fatalf("revocation presented a scheduled end date: %+v, %v", revoked, err)
	}
	unrelated, err := current.NativeOverview(ctx, other, family, Sandbox, Apple, end)
	if err != nil || unrelated.Management != nil || unrelated.Subscription != nil {
		t.Fatalf("a different owner could read purchase management data: %+v, %v", unrelated, err)
	}
}
