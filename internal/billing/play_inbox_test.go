package billing

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestPlayInboxRetainsVerifiedNoticeEncryptedAcrossRestart(t *testing.T) {
	db, j, now := fixture(t)
	ctx := t.Context()
	a, err := j.Reserve(ctx, owner, Sandbox, Play, Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	const token = "fixture-private-play-token"
	p := ProviderProof{App: "com.example.fixture", Product: "premium", BasePlan: "monthly", Region: "BE",
		Period: VerifiedPeriod{Store: Play, Environment: Sandbox, Binding: a.Binding, Subscription: playTokenDigest(token),
			Transaction: "GPA.1234-5678-9012-34567", Interval: Monthly, StartsAt: now,
			PaidThrough: now.Add(time.Hour), ObservedAt: now}}
	fake := &pollingVerifier{restoreProvider: restoreProvider{proof: p}}
	key := bytes.Repeat([]byte{1}, 32)
	offer := Offer{Store: Play, Environment: Sandbox, App: p.App, Product: p.Product, BasePlan: p.BasePlan, Interval: Monthly}
	inbox, err := NewPlayInbox(j, Sandbox, []Offer{offer}, fake, key)
	if err != nil {
		t.Fatal(err)
	}
	n := PlayNotice{ID: "receipt-1", Token: token}
	if err := inbox.Retain(ctx, n, now); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("stored a notice for an unlaunched checkout", err)
	}
	if err := j.Launch(ctx, a, now); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := inbox.Retain(ctx, n, now); err != nil {
			t.Fatal(err)
		}
	}

	var ciphertext []byte
	var digest, binding string
	if err := db.QueryRowContext(ctx, "SELECT ciphertext,token_digest,binding FROM billing_play_notices").Scan(&ciphertext, &digest, &binding); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(token)) || digest != p.Period.Subscription || binding != a.Binding {
		t.Fatal("protected queue did not retain minimal encrypted routing data")
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM billing_play_notices").Scan(&count); err != nil || count != 1 {
		t.Fatal("the same notice created duplicate work", count, err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE billing_play_notices SET lease='obsolete',lease_ns=?", timeNS(now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	current, err := StartRuntime(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := inbox.Retain(ctx, n, now); !errors.Is(err, ErrUnresolved) {
		t.Fatal("obsolete worker accepted a notice after runtime replacement", err)
	}
	var lease string
	if err := db.QueryRowContext(ctx, "SELECT lease FROM billing_play_notices").Scan(&lease); err != nil || lease != "" {
		t.Fatal("runtime replacement did not release the old lease", err)
	}
	reopened, err := NewPlayInbox(current, Sandbox, []Offer{offer}, fake, key)
	if err != nil {
		t.Fatal(err)
	}
	cleartext, err := reopened.open(n.ID, binding, digest, ciphertext)
	if err != nil || cleartext != token {
		t.Fatal("restart lost the encrypted notice")
	}
	if _, err := reopened.open("receipt-2", binding, digest, ciphertext); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("ciphertext accepted in a different event")
	}
	fake.proof.Period.Subscription = playTokenDigest("other-verified-token")
	if err := reopened.Retain(ctx, PlayNotice{ID: n.ID, Token: "other-verified-token"}, now); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("duplicate event ID accepted a different purchase", err)
	}
	if period, err := current.Active(ctx, owner, Sandbox, now); err != nil || period != nil {
		t.Fatal("notice retention granted Premium", err)
	}
	fake.proof.Period.Subscription = digest
	if _, err := db.ExecContext(ctx, "INSERT INTO lifecycle_deletions(incarnation,state) VALUES(?,'prepared')", owner); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Retain(ctx, n, now); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("redelivery bypassed the owner deletion fence", err)
	}
}
