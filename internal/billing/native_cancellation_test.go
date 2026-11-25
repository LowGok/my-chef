package billing

import (
	"context"
	"errors"
	"testing"
	"time"
)

type nativeCancelVerifier func(context.Context, NativeCancellation, []byte) error

func (v nativeCancelVerifier) VerifyCancellation(ctx context.Context, claim NativeCancellation, proof []byte) error {
	return v(ctx, claim, proof)
}

func liveNativeTestSession(commit func() error) error { return commit() }

func TestNativeCancellationRequiresOriginalProofAndConsumesItOnce(t *testing.T) {
	const family = "99998888777766665555444433332222"
	ctx := t.Context()
	_, j, now := fixture(t)
	a, err := j.ReserveNative(ctx, owner, family, Sandbox, Apple, Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := j.LaunchForNativeCancellation(ctx, a, owner, family, [32]byte{1}, now)
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now.Add(time.Minute) }
	valid := nativeCancelVerifier(func(_ context.Context, checked NativeCancellation, proof []byte) error {
		if checked != claim || string(proof) != "synthetic verified store callback" {
			return ErrPurchaseBinding
		}
		return nil
	})
	for _, tc := range []struct {
		name     string
		change   func(*NativeCancellation)
		proof    string
		verifier NativeCancellationVerifier
		at       func() time.Time
	}{
		{"unconfigured verifier", nil, "synthetic verified store callback", nil, clock},
		{"assertion rejected", nil, "unverified cancellation", valid, clock},
		{"different installation", func(c *NativeCancellation) { c.Family = owner }, "synthetic verified store callback", valid, clock},
		{"different account", func(c *NativeCancellation) { c.Owner = family }, "synthetic verified store callback", valid, clock},
		{"different attested app instance", func(c *NativeCancellation) { c.AppInstance[0] ^= 3 }, "synthetic verified store callback", valid, clock},
		{"different challenge", func(c *NativeCancellation) { c.Challenge[0] ^= 1 }, "synthetic verified store callback", valid, clock},
		{"at expiry", nil, "synthetic verified store callback", valid, func() time.Time { return now.Add(ReservationLifetime) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := claim
			if tc.change != nil {
				tc.change(&copy)
			}
			if err := j.CompleteNativeCancellation(ctx, copy, []byte(tc.proof), tc.verifier, liveNativeTestSession, tc.at); err == nil {
				t.Fatal("an unverified local cancellation released checkout")
			}
			if _, err := j.ReserveNative(ctx, owner, family, Sandbox, Play, Annual, clock()); !errors.Is(err, ErrCheckoutBusy) {
				t.Fatal("an unresolved checkout let a second store open", err)
			}
		})
	}
	proof := []byte("synthetic verified store callback")
	if err := j.CompleteNativeCancellation(ctx, claim, proof, valid, liveNativeTestSession, clock); err != nil {
		t.Fatal(err)
	}
	next, err := j.ReserveNative(ctx, owner, family, Sandbox, Play, Annual, clock())
	if err != nil || next.Binding == a.Binding {
		t.Fatal("verified cancellation did not allow a fresh checkout", err)
	}
	if err := j.CompleteNativeCancellation(ctx, claim, proof, valid, liveNativeTestSession, clock); err == nil {
		t.Fatal("replayed cancellation was accepted")
	}
	if _, err := j.ReserveNative(ctx, owner, family, Sandbox, Apple, Monthly, clock()); !errors.Is(err, ErrCheckoutBusy) {
		t.Fatal("replayed cancellation changed the new checkout", err)
	}
}

func TestNativeCancellationRechecksCompetingStoreEventAndRuntime(t *testing.T) {
	const family = "99998888777766665555444433332222"
	for _, event := range []string{"provider event", "verified payment", "Play pending purchase", "runtime restart", "verification exceeded window", "session signed out"} {
		t.Run(event, func(t *testing.T) {
			db, j, now := fixture(t)
			ctx := t.Context()
			store := Apple
			if event == "Play pending purchase" {
				store = Play
			}
			a, err := j.ReserveNative(ctx, owner, family, Sandbox, store, Monthly, now)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := j.LaunchForNativeCancellation(ctx, a, owner, family, [32]byte{1}, now)
			if err != nil {
				t.Fatal(err)
			}
			clock := now.Add(time.Minute)
			restarted := j
			sessionRevoked := false
			verifier := nativeCancelVerifier(func(ctx context.Context, _ NativeCancellation, _ []byte) error {
				switch event {
				case "provider event":
					err := j.EnqueueAppleNotice(ctx, AppleNotice{Environment: Sandbox, ID: "99887766-5544-4321-8123-4567890abcde", Binding: a.Binding, Transaction: "123456"}, clock)
					if err != nil {
						t.Fatal(err)
					}
				case "verified payment":
					err := j.Fulfil(ctx, VerifiedPeriod{Store: Apple, Environment: Sandbox, Binding: a.Binding, Subscription: "123456", Transaction: "123456",
						Interval: Monthly, StartsAt: now, PaidThrough: now.Add(2 * time.Hour), ObservedAt: clock})
					if err != nil {
						t.Fatal(err)
					}
				case "Play pending purchase":
					if _, err := db.ExecContext(ctx, `INSERT INTO billing_play_signups(binding,token_digest) VALUES(?,?)`, a.Binding, playTokenDigest("fixture-pending-token")); err != nil {
						t.Fatal(err)
					}
				case "session signed out":
					sessionRevoked = true
				case "runtime restart":
					var err error
					restarted, err = StartRuntime(ctx, db)
					if err != nil {
						t.Fatal(err)
					}
				case "verification exceeded window":
					clock = now.Add(ReservationLifetime)
				}
				return nil
			})
			fence := func(commit func() error) error {
				if sessionRevoked {
					return ErrPurchaseBinding
				}
				return commit()
			}
			if err := j.CompleteNativeCancellation(ctx, claim, []byte("synthetic proof"), verifier, fence, func() time.Time { return clock }); err == nil {
				t.Fatal("a racing or expired cancellation released the attempted purchase")
			}
			_, err = restarted.ReserveNative(ctx, owner, family, Sandbox, Play, Annual, clock)
			want := ErrCheckoutBusy
			if event == "verified payment" {
				want = ErrAlreadyPremium
			}
			if !errors.Is(err, want) {
				t.Fatal("the original purchase no longer protected the account", err)
			}
		})
	}
}
