package billing

import (
	"bytes"
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestPlayPollingRecoversMissedRenewalAndRevocation(t *testing.T) {
	db, j, now := fixture(t)
	ctx := t.Context()
	a, err := j.Reserve(ctx, owner, Sandbox, Play, Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Launch(ctx, a, now); err != nil {
		t.Fatal(err)
	}
	const token = "fixture-known-purchase"
	proof := ProviderProof{App: "com.example.fixture", Product: "premium", BasePlan: "monthly", Region: "BE",
		Period: VerifiedPeriod{Store: Play, Environment: Sandbox, Binding: a.Binding, Subscription: playTokenDigest(token),
			Transaction: "GPA.1234-5678-9012-34567", Interval: Monthly, StartsAt: now, PaidThrough: now.Add(15 * time.Minute), ObservedAt: now}}
	fake := &pollingVerifier{restoreProvider: restoreProvider{proof: proof}}
	offer := Offer{Store: Play, Environment: Sandbox, App: proof.App, Product: proof.Product, BasePlan: proof.BasePlan, Interval: Monthly}
	offers := []Offer{offer}
	key := bytes.Repeat([]byte{13}, 32)
	i, err := NewPlayInbox(j, Sandbox, offers, fake, key)
	if err != nil {
		t.Fatal(err)
	}
	// A failed poll registration cannot commit the purchase or let a device
	// finish it. The encrypted reference and the grant share one transaction.
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_play_poll BEFORE INSERT ON billing_play_polls BEGIN
 SELECT RAISE(ABORT,'fixture full disk'); END`); err != nil {
		t.Fatal(err)
	}
	if result, err := i.ReconcileCheckout(ctx, owner, a, offer, token, nil); err == nil || result.CompleteStoreTransaction {
		t.Fatal("a rejected polling write allowed store completion")
	}
	if p, err := j.Active(ctx, owner, Sandbox, now); err != nil || p != nil {
		t.Fatal("the failed encrypted reference write left a grant", err)
	}
	if _, err := db.ExecContext(ctx, "DROP TRIGGER reject_play_poll"); err != nil {
		t.Fatal(err)
	}
	if result, err := i.ReconcileCheckout(ctx, owner, a, offer, token, nil); err != nil || !result.CompleteStoreTransaction {
		t.Fatal("device result did not commit with its poll reference", err)
	}
	var cipher []byte
	if err := db.QueryRowContext(ctx, "SELECT ciphertext FROM billing_play_polls").Scan(&cipher); err != nil || bytes.Contains(cipher, []byte(token)) {
		t.Fatal("known purchase reference was not encrypted", err)
	}
	clock := proof.Period.PaidThrough
	next := proof.Period
	next.Transaction += "..0"
	next.StartsAt, next.PaidThrough, next.ObservedAt = clock, clock.Add(time.Hour), clock
	fake.proof.Period = next
	acks := 0
	r := PlayReconciler{Inbox: i, Now: func() time.Time { return clock },
		WithLiveOwner: func(_ context.Context, who string, commit func() error) error {
			if who != owner {
				t.Fatal("poll crossed account boundaries")
			}
			return commit()
		}, Acknowledge: func(_ context.Context, raw, accountBinding string) error {
			if raw != token || accountBinding != a.Binding {
				t.Fatal("poll lost the committed purchase reference")
			}
			acks++
			return nil
		}}
	// No Pub/Sub notice or client call was supplied for the renewal.
	fake.err = ErrStoreUnavailable
	if batch, err := r.PollKnown(ctx); err != nil || batch.Processed != 1 {
		t.Fatal("no known reference was polled", batch, err)
	}
	if p, err := j.Active(ctx, owner, Sandbox, clock); err != nil || p != nil || acks != 0 {
		t.Fatal("verification failure extended access", err)
	}
	reopened, err := NewPlayInbox(NewJournal(db), Sandbox, offers, fake, key)
	if err != nil {
		t.Fatal(err)
	}
	r.Inbox = reopened
	if batch, err := r.PollKnown(ctx); err != nil || batch.Processed != 0 {
		t.Fatal("poll lost its retry delay across restart", batch, err)
	}
	fake.err = nil
	clock = clock.Add(time.Minute)
	if batch, err := r.PollKnown(ctx); err != nil || batch.Processed != 1 || acks != 1 {
		t.Fatal("missing renewal was not recovered", batch, err)
	}
	if p, err := j.Active(ctx, owner, Sandbox, clock); err != nil || p == nil || p.Transaction != next.Transaction {
		t.Fatal("verified renewal did not become active", err)
	}
	clock = clock.Add(15 * time.Minute)
	fake.proof.Period.Revoked = true
	fake.proof.Period.ObservedAt = clock
	if batch, err := r.PollKnown(ctx); err != nil || batch.Processed != 1 {
		t.Fatal("revocation poll did not run", batch, err)
	}
	if p, err := j.Active(ctx, owner, Sandbox, clock); err != nil || p != nil {
		t.Fatal("authoritative revocation left Premium active", err)
	}
	const operation = "fixture-play-account-deletion"
	if _, err := db.ExecContext(ctx, "ALTER TABLE lifecycle_deletions ADD COLUMN operation TEXT"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO lifecycle_deletions(incarnation,state,operation) VALUES(?,'prepared',?)", owner, operation); err != nil {
		t.Fatal(err)
	}
	if err := transaction(ctx, db, func(tx *sql.Tx) error {
		return PurgeForDeletion(ctx, tx, operation, owner)
	}); err != nil {
		t.Fatal("could not purge the Play polling reference", err)
	}
	var remaining int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM billing_play_polls WHERE binding=?", a.Binding).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("the deleted account left an encrypted purchase reference", remaining, err)
	}
}
