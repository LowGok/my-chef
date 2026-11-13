package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func entitlementIdentity(t *testing.T, s *Store, user int64) string {
	t.Helper()
	var id string
	if err := s.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=?", user).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEntitlementGrantIdentityAndResolution(t *testing.T) {
	s, first, second, _, now := personalFixture(t)
	now = now.UTC().Truncate(time.Second)
	ctx := t.Context()
	owner := entitlementIdentity(t, s, first.UserID)
	other := entitlementIdentity(t, s, second.UserID)
	lease := FixtureEntitlementLease{ID: "lease-one", Expires: now.Add(time.Hour)}
	var generation string
	if err := s.SQL.QueryRow("UPDATE entitlement_fixture SET lease_id=?, lease_expires_ns=? RETURNING generation", lease.ID, lease.Expires.UnixNano()).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	grant := entitlementGrant{id: "opaque-one", incarnation: owner, source: "fixture", generation: generation, lease: lease.ID, start: now.UnixNano(), end: now.Add(time.Minute).UnixNano()}
	put := func(g entitlementGrant) error { return insertEntitlementGrant(s.DB.WithContext(ctx), g) }
	for range 2 {
		if err := put(grant); err != nil {
			t.Fatal(err)
		}
	}
	changed := grant
	changed.incarnation = other
	if err := put(changed); !errors.Is(err, errEntitlementConflict) {
		t.Fatal("identity reassigned", err)
	}
	changed = grant
	changed.end++
	if err := put(changed); !errors.Is(err, errEntitlementConflict) {
		t.Fatal("identity mutated", err)
	}
	for _, invalid := range []entitlementGrant{
		{id: "", incarnation: owner, source: "fixture", generation: generation, lease: lease.ID, start: grant.start, end: grant.end},
		{id: "backwards", incarnation: owner, source: "fixture", generation: generation, lease: lease.ID, start: grant.start, end: grant.start},
		{id: "orphan", incarnation: "orphan", source: "fixture", generation: generation, lease: lease.ID, start: grant.start, end: grant.end},
	} {
		if err := put(invalid); err == nil {
			t.Fatal("invalid grant stored", invalid.id)
		}
	}
	check := func(user int64, incarnation string, mode FixtureEntitlementLease, at time.Time, premium bool) {
		t.Helper()
		got, err := resolveEntitlement(ctx, s.DB, user, incarnation, mode, at)
		if err != nil || got.Premium != premium {
			t.Fatalf("at %s want Premium=%v: got %+v, %v", at, premium, got, err)
		}
	}
	check(first.UserID, owner, lease, now.Add(-time.Nanosecond), false)
	check(first.UserID, owner, lease, now, true)
	check(first.UserID, owner, lease, now.Add(time.Minute-time.Nanosecond), true)
	check(first.UserID, owner, lease, now.Add(time.Minute), false)
	check(second.UserID, owner, lease, now, false)
	check(first.UserID, other, lease, now, false)
	check(first.UserID, owner, FixtureEntitlementLease{}, now, false)
	check(first.UserID, owner, FixtureEntitlementLease{ID: "wrong", Expires: lease.Expires}, now, false)
	check(first.UserID, owner, lease, lease.Expires, false)

	overlap := grant
	overlap.id, overlap.end = "opaque-two", now.Add(2*time.Minute).UnixNano()
	unknown := overlap
	unknown.id, unknown.source = "opaque-unknown", "future-provider"
	for _, g := range []entitlementGrant{overlap, unknown} {
		if err := put(g); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DB.Exec("UPDATE entitlement_grants SET revoked_ns=? WHERE id=?", grant.start, grant.id).Error; err != nil {
		t.Fatal(err)
	}
	if err := put(grant); err != nil {
		t.Fatal("revoked retry should be idempotent", err)
	}
	check(first.UserID, owner, lease, now, true)
	if err := s.DB.Exec("UPDATE entitlement_grants SET revoked_ns=? WHERE id=?", grant.start, overlap.id).Error; err != nil {
		t.Fatal(err)
	}
	check(first.UserID, owner, lease, now, false)
	if err := s.DB.Exec("UPDATE entitlement_grants SET revoked_ns=NULL WHERE id=?", grant.id).Error; err == nil {
		t.Fatal("revoked grant reactivated")
	}
	if err := s.DB.Exec("DROP TABLE entitlement_grants").Error; err != nil {
		t.Fatal(err)
	}
	got, err := resolveEntitlement(ctx, s.DB, first.UserID, owner, lease, now)
	if err == nil || got.Premium {
		t.Fatal("lookup failure did not fail closed", got, err)
	}
}

func TestFixtureEntitlementReceiptBudgetReservesRevocation(t *testing.T) {
	s, actor, _, _, now := personalFixture(t)
	ctx := t.Context()
	if err := s.DB.Exec("UPDATE users SET email='fixture@example.test',email_key='fixture@example.test' WHERE user_id=?", actor.UserID).Error; err != nil {
		t.Fatal(err)
	}
	owner := entitlementIdentity(t, s, actor.UserID)
	lease := FixtureEntitlementLease{ID: "budget-lease", Expires: now.Add(time.Hour)}
	generation, err := s.FixtureEntitlementGeneration(ctx, lease, fixedEntitlementClock(now))
	if err != nil {
		t.Fatal(err)
	}
	mutation := func(i int, state string) error {
		return s.SetFixtureEntitlement(ctx, lease, FixtureEntitlementOperation{Generation: generation, Incarnation: owner, UserID: actor.UserID, ID: fmt.Sprintf("%032x", i), State: state}, fixedEntitlementClock(now))
	}
	for i := 1; i <= 1024; i++ {
		if err := mutation(i, "premium"); err != nil {
			t.Fatalf("ordinary operation %d: %v", i, err)
		}
	}
	if err := mutation(1025, "premium"); err == nil {
		t.Fatal("saturated generation granted more access")
	}
	if err := mutation(1025, "free"); err != nil {
		t.Fatal("saturation prevented revocation", err)
	}
	for _, retry := range []struct {
		i     int
		state string
	}{{1, "premium"}, {1025, "free"}} {
		if err := mutation(retry.i, retry.state); err != nil {
			t.Fatal("retry rejected after saturation", err)
		}
	}
	if err := mutation(1026, "free"); err == nil {
		t.Fatal("new revocation for Free account used reserved capacity")
	}
	premium, err := resolveEntitlement(ctx, s.DB, actor.UserID, owner, lease, now)
	if err != nil || premium.Premium {
		t.Fatal("saturated retry reactivated grant", err)
	}
	for sql, want := range map[string]int{
		"SELECT COUNT(*) FROM entitlement_grants":                              1024,
		"SELECT COUNT(*) FROM entitlement_fixture_operations WHERE reserved=0": 1024,
		"SELECT COUNT(*) FROM entitlement_fixture_operations WHERE reserved=1": 1,
	} {
		var count int
		if err := s.SQL.QueryRow(sql).Scan(&count); err != nil || count != want {
			t.Fatalf("expected %d rows, got %d: %v", want, count, err)
		}
	}
}

func testEntitlementSwitch(t *testing.T, s *Store, actor PersonalActor, owner string, clock func() time.Time) (PersonalEntitlementReader, func()) {
	t.Helper()
	lease := FixtureEntitlementLease{ID: "test-lease", Expires: clock().Add(time.Minute)}
	generation, err := s.FixtureEntitlementGeneration(t.Context(), lease, clock)
	if err != nil {
		t.Fatal(err)
	}
	op := FixtureEntitlementOperation{ID: strings.Repeat("a", 32), Generation: generation, Incarnation: owner, State: "premium", UserID: actor.UserID}
	if err := s.SetFixtureEntitlement(t.Context(), lease, op, clock); err != nil {
		t.Fatal(err)
	}
	return EntitlementReader(lease, clock), func() {
		t.Helper()
		op.ID, op.State = strings.Repeat("b", 32), "free"
		if err := s.SetFixtureEntitlement(t.Context(), lease, op, clock); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFixtureEntitlementCapsDistinctAccountsAndDoesNotRefundReceipts(t *testing.T) {
	s, _, _, _, now := personalFixture(t)
	ctx := t.Context()
	lease := FixtureEntitlementLease{ID: "distinct-account-lease", Expires: now.Add(time.Hour)}
	generation, err := s.FixtureEntitlementGeneration(ctx, lease, fixedEntitlementClock(now))
	if err != nil {
		t.Fatal(err)
	}
	ops := make([]FixtureEntitlementOperation, 257)
	for i := range ops {
		address := fmt.Sprintf("fixture%d@example.test", i)
		user := User{FirstName: "Test", LastName: "Cook", Email: address, EmailKey: address, Username: fmt.Sprintf("fixture%d", i), Password: "fixture-hash", EmailVerifiedAt: Ptr(now.Unix())}
		if err := s.DB.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		ops[i] = FixtureEntitlementOperation{ID: strings.Repeat("a", 32), Generation: generation, Incarnation: entitlementIdentity(t, s, user.ID), UserID: user.ID, State: "premium"}
	}
	for _, op := range ops[:256] {
		if err := s.SetFixtureEntitlement(ctx, lease, op, fixedEntitlementClock(now)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetFixtureEntitlement(ctx, lease, ops[256], fixedEntitlementClock(now)); !errors.Is(err, ErrPersonalPermission) {
		t.Fatal("too many fixture accounts granted", err)
	}
	if err := s.DB.Delete(&User{}, ops[0].UserID).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.SetFixtureEntitlement(ctx, lease, ops[256], fixedEntitlementClock(now)); err != nil {
		t.Fatal("deleted grant did not free account capacity", err)
	}
	var used, receipts int
	if err := s.SQL.QueryRow("SELECT ordinary_used,(SELECT COUNT(*) FROM entitlement_fixture_operations) FROM entitlement_fixture").Scan(&used, &receipts); err != nil || used != 257 || receipts != 256 {
		t.Fatal("deletion did not purge owned receipts or refunded generation budget", used, receipts, err)
	}
}

func fixedEntitlementClock(now time.Time) func() time.Time { return func() time.Time { return now } }

func TestFixtureEntitlementRejectsLeaseExpiredAtWrite(t *testing.T) {
	s, actor, _, _, now := personalFixture(t)
	if err := s.DB.Exec("UPDATE users SET email='expiry-fixture@example.test',email_key='expiry-fixture@example.test' WHERE user_id=?", actor.UserID).Error; err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	lease := FixtureEntitlementLease{ID: "short-lease", Expires: now.Add(time.Second)}
	generation, err := s.FixtureEntitlementGeneration(ctx, lease, fixedEntitlementClock(now))
	if err != nil {
		t.Fatal(err)
	}
	op := FixtureEntitlementOperation{ID: strings.Repeat("a", 32), Generation: generation, Incarnation: entitlementIdentity(t, s, actor.UserID), UserID: actor.UserID, State: "premium"}
	if err := s.SetFixtureEntitlement(ctx, lease, op, fixedEntitlementClock(lease.Expires)); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatal("expired fixture created a grant", err)
	}
	var count int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM entitlement_fixture_operations").Scan(&count); err != nil || count != 0 {
		t.Fatal("expired fixture wrote a receipt", count, err)
	}
}
