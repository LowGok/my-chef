package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/billing"
)

func TestBillingDeletionFencesLateProofAndScrubsOnlyTheDeletedOwner(t *testing.T) {
	s, j, actor := accountDeletionFixture(t)
	ctx := t.Context()
	inc := entitlementIdentity(t, s, actor.UserID)
	other := entitlementIdentity(t, s, 2)
	b, err := j.Billing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	seed := func(who, ref string) billing.VerifiedPeriod {
		t.Helper()
		a, err := b.Reserve(ctx, who, billing.Sandbox, billing.Apple, billing.Monthly, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Launch(ctx, a, now); err != nil {
			t.Fatal(err)
		}
		p := billing.VerifiedPeriod{Store: billing.Apple, Environment: billing.Sandbox, Binding: a.Binding, Subscription: ref,
			Transaction: ref, Interval: billing.Monthly, StartsAt: now, PaidThrough: now.Add(time.Hour), ObservedAt: now,
			Renewal: &billing.RenewalObservation{State: billing.RenewalEnds, ObservedAt: now}}
		if err := b.Fulfil(ctx, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old := seed(inc, "98701")
	_ = seed(other, "98702")
	for i, id := range []string{inc, other} {
		key := make([]byte, 32)
		key[0] = byte(i + 1)
		if _, err := j.store.SQL.ExecContext(ctx, `INSERT INTO billing_apple_attested_keys
 (incarnation,family,environment,scope,key_id,point,counter) VALUES(?,?,'sandbox',zeroblob(32),?,zeroblob(65),1)`, id, id, key); err != nil {
			t.Fatal(err)
		}
		if _, err := j.store.SQL.ExecContext(ctx, `INSERT INTO billing_apple_enrollments
 (incarnation,family,environment,scope,digest,issued_ns,expires_ns,generation)
 VALUES(?,?,'sandbox',zeroblob(32),zeroblob(32),1,2,'synthetic')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.EnqueueAppleNotice(ctx, billing.AppleNotice{Environment: billing.Sandbox, ID: "99887766-5544-4321-8123-4567890abc01", Binding: old.Binding, Transaction: "98703"}, now); err != nil {
		t.Fatal(err)
	}
	ops := AccountDeletionOptions{Fault: func(stage string) error {
		if stage != "prepared" {
			return nil
		}
		if got, err := b.Active(ctx, inc, billing.Sandbox, now); err != nil || got != nil {
			t.Fatalf("prepared deletion still granted Premium: %+v %v", got, err)
		}
		if err := b.Fulfil(ctx, old); err == nil {
			t.Fatal("late callback won after deletion prepared")
		}
		return errors.New("synthetic pre-commit rejection")
	}}
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("3", 32), "DELETE", now, ops); !errors.Is(err, ErrAccountDeletion) {
		t.Fatal("pre-commit failure was not returned", err)
	}
	if got, err := b.Active(ctx, inc, billing.Sandbox, now); err != nil || got == nil {
		t.Fatal("confirmed rollback left an active account fenced", err)
	}
	op := strings.Repeat("4", 32)
	if _, err := j.DeleteAccount(ctx, actor, op, "DELETE", now, AccountDeletionOptions{Fault: func(stage string) error {
		if stage == "sqlite" {
			return errors.New("synthetic lost process after commit")
		}
		return nil
	}}); !errors.Is(err, ErrLifecycleUncertain) {
		t.Fatal("lost completion was not retained for restart", err)
	}
	if got, err := b.Active(ctx, inc, billing.Sandbox, now); err != nil || got != nil {
		t.Fatal("unfinalized commit exposed access", err)
	}
	if _, err := j.store.SQL.ExecContext(ctx, `CREATE TRIGGER billing_fixture_unavailable BEFORE DELETE ON billing_attempts BEGIN SELECT RAISE(ABORT,'fixture unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := j.ReconcileAccountDeletions(ctx, AccountDeletionOptions{}); !errors.Is(err, ErrLifecycleJournal) {
		t.Fatal("failed billing purge finalized deletion", err)
	}
	var state string
	if err := j.store.SQL.QueryRowContext(ctx, `SELECT state FROM lifecycle_deletions WHERE operation=?`, op).Scan(&state); err != nil || state != "prepared" {
		t.Fatal("failed finalization lost the prepared fence", err)
	}
	if _, err := j.store.SQL.ExecContext(ctx, `DROP TRIGGER billing_fixture_unavailable`); err != nil {
		t.Fatal(err)
	}
	if err := j.ReconcileAccountDeletions(ctx, AccountDeletionOptions{}); err != nil {
		t.Fatal("restart did not finalize deletion", err)
	}
	if got, err := b.Active(ctx, other, billing.Sandbox, now); err != nil || got == nil {
		t.Fatal("deletion affected unrelated account", err)
	}
	for _, query := range []struct {
		sql string
		arg string
	}{
		{"SELECT count(*) FROM billing_attempts WHERE incarnation=?", inc},
		{"SELECT count(*) FROM billing_subscriptions WHERE incarnation=?", inc},
		{"SELECT count(*) FROM billing_periods WHERE proof=?", old.Transaction},
		{"SELECT count(*) FROM billing_period_renewals WHERE proof=?", old.Transaction},
		{"SELECT count(*) FROM billing_runtime_proofs WHERE proof=?", old.Transaction},
		{"SELECT count(*) FROM billing_apple_notices WHERE binding=?", old.Binding},
		{"SELECT count(*) FROM billing_apple_polls WHERE binding=?", old.Binding},
		{"SELECT count(*) FROM billing_apple_enrollments WHERE incarnation=?", inc},
		{"SELECT count(*) FROM billing_apple_attested_keys WHERE incarnation=?", inc},
	} {
		var n int
		if err := j.store.SQL.QueryRowContext(ctx, query.sql, query.arg).Scan(&n); err != nil || n != 0 {
			t.Fatal("deleted billing material remained", query.sql, err)
		}
	}
	var keptKeys int
	if err := j.store.SQL.QueryRowContext(ctx, `SELECT count(*) FROM billing_apple_attested_keys WHERE incarnation=? AND counter=1`, other).Scan(&keptKeys); err != nil || keptKeys != 1 {
		t.Fatal("deletion removed the other account's attested key", err)
	}
	var markers int
	if err := j.store.SQL.QueryRowContext(ctx, `SELECT count(*) FROM billing_replay_markers WHERE length(digest)=32`).Scan(&markers); err != nil || markers < 3 {
		t.Fatal("missing purchase replay markers", err)
	}
	if _, err := s.SQL.ExecContext(ctx, "INSERT INTO users(user_id,firstname,lastname,email,email_key,password,username) VALUES(1,'Fresh','Account','owner@example.test','owner@example.test','fresh-hash','new-owner')"); err != nil {
		t.Fatal(err)
	}
	fresh := entitlementIdentity(t, s, 1)
	newAttempt, err := b.Reserve(ctx, fresh, billing.Sandbox, billing.Apple, billing.Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Launch(ctx, newAttempt, now); err != nil {
		t.Fatal(err)
	}
	old.Binding = newAttempt.Binding
	if err := b.Fulfil(ctx, old); !errors.Is(err, billing.ErrPurchaseBinding) {
		t.Fatal("deleted original purchase was rebound to the new signup", err)
	}
}

func TestBillingDeletionStorageFailureLeavesAccountUntouched(t *testing.T) {
	s, j, actor := accountDeletionFixture(t)
	ctx := t.Context()
	if _, err := j.store.SQL.ExecContext(ctx, `DROP TABLE billing_replay_markers`); err != nil {
		t.Fatal(err)
	}
	op := strings.Repeat("5", 32)
	if _, err := j.DeleteAccount(ctx, actor, op, "DELETE", time.Now().UTC(), AccountDeletionOptions{}); !errors.Is(err, ErrLifecycleJournal) {
		t.Fatal("missing billing storage did not stop account purge", err)
	}
	var n int
	if err := s.SQL.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE user_id=?`, actor.UserID).Scan(&n); err != nil || n != 1 {
		t.Fatal("account was deleted despite a pre-commit journal error", err)
	}
	if err := j.store.SQL.QueryRowContext(ctx, `SELECT count(*) FROM lifecycle_deletions WHERE operation=?`, op).Scan(&n); err != nil || n != 0 {
		t.Fatal("partial preparation left an unnecessary fence", err)
	}
}

func TestBillingDeletionReceiptInCopyCannotFinalizeOriginalIntent(t *testing.T) {
	s, j, actor := accountDeletionFixture(t)
	ctx := t.Context()
	inc := entitlementIdentity(t, s, actor.UserID)
	op := strings.Repeat("6", 32)
	var baseline []byte
	_, err := j.DeleteAccount(ctx, actor, op, "DELETE", time.Now().UTC(), AccountDeletionOptions{Fault: func(stage string) error {
		if stage == "prepared" {
			var e error
			baseline, e = os.ReadFile(s.Path)
			if e != nil {
				t.Fatal(e)
			}
		}
		if stage == "sqlite" {
			return errors.New("stop before external finalization")
		}
		return nil
	}})
	if !errors.Is(err, ErrLifecycleUncertain) || len(baseline) == 0 {
		t.Fatal("did not capture committed app before finalization", err)
	}
	// Make a disposable alternate file with the exact committed receipt. A
	// copied receipt cannot establish the original deployment identity.
	snapshot := filepath.Join(t.TempDir(), "committed-copy.db")
	if _, err := s.SQL.ExecContext(ctx, "VACUUM INTO ?", snapshot); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(snapshot, 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored.db")
	if err := Restore(ctx, snapshot, target, AccountRestoreOptions{JournalPath: j.Path}); !errors.Is(err, ErrLifecycleUncertain) {
		t.Fatal("a receipt from a different file finalized the original deletion", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("restore published before original reconciliation", err)
	}
	var candidates int
	if err := j.store.SQL.QueryRowContext(ctx, `SELECT count(*) FROM lifecycle_deletions WHERE operation=? AND incarnation=? AND state='prepared'`, op, inc).Scan(&candidates); err != nil || candidates != 1 {
		t.Fatal("original prepared fence changed", err)
	}
	if err := j.ReconcileAccountDeletions(ctx, AccountDeletionOptions{}); err != nil {
		t.Fatal("original committed receipt could not finalize", err)
	}
	if err := j.store.SQL.QueryRowContext(ctx, `SELECT count(*) FROM billing_deletion_origins WHERE operation=?`, op).Scan(&candidates); err != nil || candidates != 0 {
		t.Fatal("finalized deletion retained the keyed database baseline", err)
	}
}

func TestBillingDeletionRefusesExactBaselineReplayedInPlace(t *testing.T) {
	s, j, actor := accountDeletionFixture(t)
	ctx := t.Context()
	op := strings.Repeat("7", 32)
	var baseline []byte
	_, err := j.DeleteAccount(ctx, actor, op, "DELETE", time.Now().UTC(), AccountDeletionOptions{Fault: func(stage string) error {
		if stage == "prepared" {
			var e error
			baseline, e = os.ReadFile(s.Path)
			if e != nil {
				t.Fatal(e)
			}
			if wal, e := os.Stat(s.Path + "-wal"); e != nil || wal.Size() != 0 {
				t.Fatal("prepared baseline was not checkpointed", e)
			}
		}
		if stage == "sqlite" {
			return errors.New("stop before the protected commit")
		}
		return nil
	}})
	if !errors.Is(err, ErrLifecycleUncertain) || len(baseline) == 0 {
		t.Fatal("lost original intent", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	// Overwrite the disposable file without changing its inode. This copies
	// the exact baseline back, including its app binding and account.
	before, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path, baseline, 0600); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(s.Path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("test did not preserve inode", err)
	}
	replayed, err := Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replayed.Close() })
	paired, err := replayed.OpenLifecycleJournal(ctx, j.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = paired.Close() })
	if err := paired.deletionOriginMatches(ctx, op); err != nil {
		t.Fatal("test did not preserve the protected origin identity", err)
	}
	if err := paired.ReconcileAccountDeletions(ctx, AccountDeletionOptions{}); !errors.Is(err, ErrLifecycleUncertain) {
		t.Fatal("same bytes established imaginary rollback provenance", err)
	}
	if err := paired.CheckServing(ctx); !errors.Is(err, ErrLifecycleUncertain) {
		t.Fatal("service accepted restored state with unresolved deletion", err)
	}
}
