package billing

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPlayReplacementRequiresTheSameProductAndNextBillingDateMode(t *testing.T) {
	const app = "com.example.fixture"
	const old = "fixture-owned-token"
	const next = "fixture-replacement-token"
	const binding = "5095aae8-27e3-45de-bef1-bf9b179cedcf"
	const active = `{"kind":"androidpublisher#subscriptionPurchaseV2","regionCode":"BE","subscriptionState":"SUBSCRIPTION_STATE_ACTIVE",
 "testPurchase":{},"linkedPurchaseToken":"` + old + `","externalAccountIdentifiers":{"obfuscatedExternalAccountId":"` + binding + `"},
 "startTime":"2026-11-01T10:00:00Z","lineItems":[{"productId":"fixture-premium","expiryTime":"2026-11-05T18:00:00Z",
 "autoRenewingPlan":{"autoRenewEnabled":true},"offerDetails":{"basePlanId":"annual"},
 "itemReplacement":{"productId":"fixture-premium","basePlanId":"monthly","replacementMode":"WITHOUT_PRORATION"},
 "offerPhase":{"basePrice":{}}}]}`
	for _, test := range []struct {
		name, data string
		err        error
	}{
		{"recognized replacement", active, nil},
		{"old full price carryover", strings.Replace(active, `"basePrice":{}`, `"prorationPeriod":{"originalOfferPhaseType":"BASE"}`, 1), nil},
		{"old trial carryover", strings.Replace(active, `"basePrice":{}`, `"prorationPeriod":{"originalOfferPhaseType":"FREE_TRIAL"}`, 1), ErrPurchaseBinding},
		{"immediate charge", strings.Replace(active, "WITHOUT_PRORATION", "CHARGE_FULL_PRICE", 1), ErrPurchaseBinding},
		{"trial", strings.Replace(active, "basePrice", "freeTrial", 1), ErrPurchaseBinding},
		{"foreign product", strings.Replace(active, `"itemReplacement":{"productId":"fixture-premium"`, `"itemReplacement":{"productId":"other-premium"`, 1), ErrPurchaseBinding},
		{"wrong old plan", strings.Replace(active, `"basePlanId":"monthly"`, `"basePlanId":"annual"`, 1), ErrPurchaseBinding},
		{"self-linked token", strings.Replace(active, old, next, 1), ErrPurchaseBinding},
		{"outside purchase", strings.Replace(active, `"testPurchase":{},`, `"testPurchase":{},"outOfAppPurchaseContext":{},`, 1), ErrPurchaseBinding},
		{"unpaid checkout", strings.Replace(active, "STATE_ACTIVE", "STATE_PENDING", 1), ErrUnresolved},
		{"live proof in sandbox", strings.Replace(active, `"testPurchase":{},`, "", 1), ErrPurchaseBinding},
		{"provider expires the replacement", strings.Replace(active, "STATE_ACTIVE", "STATE_EXPIRED", 1), ErrUnresolved},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			provider, err := NewPlayProvider(PlayConfig{Package: app, Product: "fixture-premium",
				MonthlyBasePlan: "monthly", AnnualBasePlan: "annual", Environment: Sandbox,
				AccessToken: func(context.Context) (string, error) { return "offline-fixture", nil }},
				&http.Client{Transport: playTestTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Method != http.MethodGet || r.URL.Host != "androidpublisher.googleapis.com" ||
						!strings.HasSuffix(r.URL.Path, "/purchases/subscriptionsv2/tokens/"+next) {
						t.Fatal("replacement verifier exceeded read-only scope")
					}
					return playTestReply(http.StatusOK, test.data), nil
				})}, func() time.Time { return time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC) })
			if err != nil {
				t.Fatal(err)
			}
			proof, err := provider.VerifyReplacement(t.Context(), next)
			if !errors.Is(err, test.err) || calls != 1 {
				t.Fatal("unexpected replacement result", err)
			}
			if err == nil && (proof.Previous != playTokenDigest(old) || proof.Current != playTokenDigest(next) ||
				proof.Binding != binding || proof.StoreApp != app || proof.PreviousInterval != Monthly ||
				proof.CurrentInterval != Annual || proof.Environment != Sandbox ||
				!proof.StoreExpiry.Equal(time.Date(2026, 11, 5, 18, 0, 0, 0, time.UTC))) {
				t.Fatal("verified replacement facts did not match the store")
			}
		})
	}
}

type fakePlayReplacement struct {
	restoreProvider
	replacement PlayReplacement
	linked      ProviderProof
}

func (p *fakePlayReplacement) VerifyReplacement(context.Context, string) (PlayReplacement, error) {
	return p.replacement, nil
}

func (p *fakePlayReplacement) VerifyExisting(ctx context.Context, token string) (ProviderProof, error) {
	if p.replacement.Current == playTokenDigest(token) {
		return ProviderProof{}, ErrUnresolved
	}
	return p.restoreProvider.VerifyExisting(ctx, token)
}

func (p *fakePlayReplacement) VerifyLinkedPeriod(_ context.Context, token, previous string) (ProviderProof, error) {
	if p.replacement.Current != playTokenDigest(token) || p.replacement.Previous != previous {
		return ProviderProof{}, ErrPurchaseBinding
	}
	if p.linked.Period.Transaction == "" {
		return ProviderProof{}, ErrUnresolved
	}
	return p.linked, nil
}

func TestPlayStagesReplacementWithoutChangingThePaidPeriod(t *testing.T) {
	for _, swap := range []struct {
		from, to Interval
	}{{Monthly, Annual}, {Annual, Monthly}} {
		t.Run(string(swap.from), func(t *testing.T) {
			db, j, now := fixture(t)
			ctx := t.Context()
			a, err := j.Reserve(ctx, owner, Sandbox, Play, swap.from, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := j.Launch(ctx, a, now); err != nil {
				t.Fatal(err)
			}
			const previous = "fixture-paid-token"
			const current = "fixture-plan-change-token"
			p := VerifiedPeriod{Store: Play, Environment: Sandbox, Binding: a.Binding, Subscription: playTokenDigest(previous),
				Transaction: "GPA.1234-5678-9012-34567", Interval: swap.from, StartsAt: now,
				PaidThrough: now.Add(time.Hour), ObservedAt: now}
			offer := Offer{Store: Play, Environment: Sandbox, App: "com.example.fixture", Product: "premium",
				BasePlan: string(swap.from), Interval: swap.from}
			next := offer
			next.BasePlan, next.Interval = string(swap.to), swap.to
			when := now.Add(time.Minute)
			fake := &fakePlayReplacement{restoreProvider: restoreProvider{proof: ProviderProof{
				App: offer.App, Product: offer.Product, Region: "BE", BasePlan: offer.BasePlan, Period: p,
			}}, replacement: PlayReplacement{Environment: Sandbox, StoreApp: offer.App, Product: offer.Product, Region: "BE",
				Binding: a.Binding, Previous: p.Subscription, Current: playTokenDigest(current), PreviousInterval: swap.from,
				CurrentInterval: swap.to, StoreExpiry: p.PaidThrough, StartedAt: when, ObservedAt: when}}
			inbox, err := NewPlayInbox(j, Sandbox, []Offer{offer, next}, fake, bytes.Repeat([]byte{41}, 32))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inbox.ReconcileCheckout(ctx, owner, a, offer, previous, nil); err != nil {
				t.Fatal(err)
			}
			if err := inbox.StageReplacement(ctx, strings.Repeat("f", 32), current, nil); !errors.Is(err, ErrPurchaseBinding) {
				t.Fatal("a different MyChef account inherited the Play change", err)
			}
			fake.replacement.StoreExpiry = p.PaidThrough.Add(time.Minute)
			if err := inbox.StageReplacement(ctx, owner, current, nil); !errors.Is(err, ErrPurchaseBinding) {
				t.Fatal("a different cutoff was treated as carryover", err)
			}
			fake.replacement.StoreExpiry = p.PaidThrough
			if swap.from == Monthly {
				n := PlayNotice{ID: "fixture-plan-change", Token: current}
				if _, err := db.ExecContext(ctx, `CREATE TRIGGER fixture_notice_write_failure BEFORE INSERT ON billing_play_notices BEGIN
 SELECT RAISE(ABORT,'synthetic write failure'); END`); err != nil {
					t.Fatal(err)
				}
				if err := inbox.Retain(ctx, n, when); err == nil {
					t.Fatal("a notice write failure was safe to acknowledge")
				}
				var digest string
				if err := db.QueryRowContext(ctx, "SELECT token_digest FROM billing_play_polls WHERE binding=?", a.Binding).Scan(&digest); err != nil || digest != p.Subscription {
					t.Fatal("failed notice write rotated the polling token", err)
				}
				if _, err := db.ExecContext(ctx, "DROP TRIGGER fixture_notice_write_failure"); err != nil {
					t.Fatal(err)
				}
				if err := inbox.Retain(ctx, n, when); err != nil {
					t.Fatal("store notice did not preserve the verified replacement", err)
				}
				var state string
				if err := db.QueryRowContext(ctx, "SELECT state FROM billing_play_notices WHERE event_id=?", n.ID).Scan(&state); err != nil || state != "done" {
					t.Fatal("notice was not durably attributed to the staged replacement", err)
				}
			} else {
				if result, err := inbox.Restore(ctx, owner, current, nil); !errors.Is(err, ErrUnresolved) || result.CompleteStoreTransaction {
					t.Fatal("foreground restore did not wait for the paid order", err)
				}
				var successor string
				if err := db.QueryRowContext(ctx, "SELECT successor FROM billing_play_replacements").Scan(&successor); err != nil || successor != playTokenDigest(current) {
					t.Fatal("restore did not stage the replacement", err)
				}
			}
			for range 2 {
				if err := inbox.StageReplacement(ctx, owner, current, nil); err != nil {
					t.Fatal("current owner's valid plan change was not staged", err)
				}
			}
			newToken := "fixture-second-plan-change-token"
			fake.replacement.Current = playTokenDigest(newToken)
			if err := inbox.StageReplacement(ctx, owner, newToken, nil); !errors.Is(err, ErrUnresolved) {
				t.Fatal("a different token could replace the same predecessor twice", err)
			}
			var cutoff int64
			var cipher []byte
			if err := db.QueryRowContext(ctx, "SELECT effective_ns,ciphertext FROM billing_play_replacements").Scan(&cutoff, &cipher); err != nil ||
				cutoff != timeNS(p.PaidThrough) || bytes.Contains(cipher, []byte(current)) {
				t.Fatal("replacement was not staged with only the encrypted token and current cutoff", err)
			}
			if current, err := j.Active(ctx, owner, Sandbox, when); err != nil || current == nil ||
				current.Interval != swap.from || !current.PaidThrough.Equal(p.PaidThrough) {
				t.Fatal("staged plan changed the existing paid period", err)
			}
			if current, err := j.Active(ctx, owner, Sandbox, p.PaidThrough); err != nil || current != nil {
				t.Fatal("unpaid renewal extended Premium", err)
			}
			fake.replacement.Current = playTokenDigest(current)
			clock := when
			acked := 0
			runner := PlayReconciler{Inbox: inbox, Now: func() time.Time { return clock },
				WithLiveOwner: func(_ context.Context, who string, apply func() error) error {
					if who != owner {
						t.Fatal("successor was attributed to a different account")
					}
					return apply()
				},
				Acknowledge: func(context.Context, string, string) error {
					t.Fatal("used the original purchase acknowledgement for a change")
					return nil
				},
				AcknowledgeReplacement: func(_ context.Context, raw, who, parent string) error {
					if raw != current || who != a.Binding || parent != p.Subscription {
						t.Fatal("wrong successor acknowledgement")
					}
					acked++
					return nil
				}}
			if run, err := runner.PollKnown(ctx); err != nil || run.Processed != 1 || acked != 1 {
				t.Fatal("worker did not resume the staged token before renewal", run, err)
			}
			j, err = StartRuntime(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			inbox, err = NewPlayInbox(j, Sandbox, []Offer{offer, next}, fake, bytes.Repeat([]byte{41}, 32))
			if err != nil {
				t.Fatal(err)
			}
			runner.Inbox = inbox
			if period, err := j.Active(ctx, owner, Sandbox, when); err != nil || period != nil {
				t.Fatal("restart exposed access before fresh provider proof", err)
			}
			fake.replacement.StoreExpiry = p.PaidThrough.Add(time.Minute)
			if run, err := runner.PollKnown(ctx); err != nil || run.Processed != 1 || acked != 1 {
				t.Fatal("a changed carryover cutoff was acknowledged", run, err)
			}
			if period, err := j.Active(ctx, owner, Sandbox, when); err != nil || period != nil {
				t.Fatal("a changed carryover cutoff restored access", err)
			}
			clock = clock.Add(time.Minute)
			fake.replacement.StoreExpiry, fake.replacement.ObservedAt = p.PaidThrough, clock
			if run, err := runner.PollKnown(ctx); err != nil || run.Processed != 1 || acked != 2 {
				t.Fatal("restart did not check the acknowledged successor", run, err)
			}
			if period, err := j.Active(ctx, owner, Sandbox, when); err != nil || period == nil || period.Interval != swap.from ||
				!period.PaidThrough.Equal(p.PaidThrough) {
				t.Fatal("fresh replacement proof did not recover the already paid period", err)
			}
			if _, err := inbox.Restore(ctx, owner, previous, nil); !errors.Is(err, ErrPurchaseBinding) {
				t.Fatal("an obsolete token could still be restored", err)
			}
			clock = p.PaidThrough
			if run, err := runner.PollKnown(ctx); err != nil || run.Processed != 1 {
				t.Fatal("missing payment was not reconciled", run, err)
			}
			if current, err := j.Active(ctx, owner, Sandbox, clock); err != nil || current != nil {
				t.Fatal("an unfinished successor kept paid access", err)
			}
			clock = clock.Add(time.Minute)
			nextPeriod := p
			nextPeriod.Subscription, nextPeriod.Transaction, nextPeriod.Interval = playTokenDigest(current), "GPA.1234-5678-9012-34567..0", swap.to
			nextPeriod.StartsAt, nextPeriod.PaidThrough, nextPeriod.ObservedAt = p.PaidThrough, p.PaidThrough.Add(time.Hour), clock
			fake.linked = ProviderProof{App: next.App, Product: next.Product, BasePlan: next.BasePlan, Region: "BE", Period: nextPeriod}
			if run, err := runner.PollKnown(ctx); err != nil || run.Processed != 1 {
				t.Fatal("paid successor was not reconciled", run, err)
			}
			if got, err := j.Active(ctx, owner, Sandbox, clock); err != nil || got == nil ||
				got.Interval != swap.to || got.Subscription != p.Subscription || !got.PaidThrough.Equal(nextPeriod.PaidThrough) {
				t.Fatal("the paid successor did not retain the original owner's lineage", err)
			}

		})
	}
}

func TestLinkedPlayPeriodRequiresItsOwnPaidOrder(t *testing.T) {
	const app = "com.example.fixture"
	const previous = "fixture-prior-token"
	const token = "fixture-current-token"
	const binding = "5095aae8-27e3-45de-bef1-bf9b179cedcf"
	const order = "GPA.1234-5678-9012-34567..0"
	const sub = `{"kind":"androidpublisher#subscriptionPurchaseV2","regionCode":"BE","subscriptionState":"SUBSCRIPTION_STATE_ACTIVE",
 "testPurchase":{},"linkedPurchaseToken":"` + previous + `","externalAccountIdentifiers":{"obfuscatedExternalAccountId":"` + binding + `"},
 "lineItems":[{"productId":"premium","expiryTime":"2027-11-12T18:00:00Z","latestSuccessfulOrderId":"` + order + `",
 "autoRenewingPlan":{},"offerDetails":{"basePlanId":"annual"},"offerPhase":{"basePrice":{}}}]}`
	const payment = `{"orderId":"` + order + `","purchaseToken":"` + token + `","state":"PROCESSED",
 "lineItems":[{"productId":"premium","subscriptionDetails":{"basePlanId":"annual","offerPhaseDetails":{"baseDetails":{}},
 "servicePeriodStartTime":"2026-11-12T18:00:00Z","servicePeriodEndTime":"2027-11-12T18:00:00Z"}}]}`
	for _, tc := range []struct {
		name, parent, order string
		want                error
	}{
		{"paid successor", previous, payment, nil},
		{"foreign saved predecessor", "another-prior-token", payment, ErrPurchaseBinding},
		{"old order", previous, strings.Replace(payment, token, previous, 1), ErrPurchaseBinding},
		{"pending renewal", previous, strings.Replace(payment, "PROCESSED", "PENDING", 1), ErrUnresolved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, err := NewPlayProvider(PlayConfig{Package: app, Product: "premium", MonthlyBasePlan: "monthly",
				AnnualBasePlan: "annual", Environment: Sandbox,
				AccessToken: func(context.Context) (string, error) { return "offline-provider-token", nil }},
				&http.Client{Transport: playTestTransport(func(r *http.Request) (*http.Response, error) {
					if r.Method == http.MethodGet && r.URL.Path == "/androidpublisher/v3/applications/"+app+"/purchases/subscriptionsv2/tokens/"+token {
						return playTestReply(200, sub), nil
					}
					if r.Method == http.MethodGet && r.URL.Path == "/androidpublisher/v3/applications/"+app+"/orders/"+order {
						return playTestReply(200, tc.order), nil
					}
					t.Fatal("unexpected linked purchase route")
					return nil, ErrStoreUnavailable
				})}, func() time.Time { return time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC) })
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Verify(t.Context(), token); !errors.Is(err, ErrUnresolved) {
				t.Fatal("linked token entered first checkout verification", err)
			}
			proof, err := provider.VerifyLinkedPeriod(t.Context(), token, playTokenDigest(tc.parent))
			if !errors.Is(err, tc.want) {
				t.Fatal("unexpected linked order response", err)
			}
			if err == nil && (proof.Period.Transaction != order || proof.Period.Subscription != playTokenDigest(token) ||
				proof.Period.Binding != binding || proof.Period.Interval != Annual) {
				t.Fatal("successful proof did not belong to the linked order")
			}
		})
	}
}

func TestPlayRecoversReplacementFirstSeenAfterItsRenewal(t *testing.T) {
	db, j, now := fixture(t)
	ctx := t.Context()
	const previous = "fixture-previous"
	const successor = "fixture-successor"
	a, err := j.Reserve(ctx, owner, Sandbox, Play, Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Launch(ctx, a, now); err != nil {
		t.Fatal(err)
	}
	offer := Offer{Store: Play, Environment: Sandbox, App: "com.example.fixture", Product: "premium", Interval: Monthly, BasePlan: "monthly"}
	next := offer
	next.Interval, next.BasePlan = Annual, "annual"
	cutoff := now.Add(time.Hour)
	seen := cutoff.Add(time.Minute)
	newExpiry := cutoff.Add(365 * 24 * time.Hour)
	oldPeriod := VerifiedPeriod{Store: Play, Environment: Sandbox, Binding: a.Binding, Subscription: playTokenDigest(previous),
		Transaction: "GPA.1234-5678-9012-34567", Interval: Monthly, StartsAt: now, PaidThrough: cutoff, ObservedAt: now}
	newPeriod := VerifiedPeriod{Store: Play, Environment: Sandbox, Binding: a.Binding, Subscription: playTokenDigest(successor),
		Transaction: "GPA.1234-5678-9012-34567..0", Interval: Annual, StartsAt: cutoff, PaidThrough: newExpiry, ObservedAt: seen}
	fake := &fakePlayReplacement{restoreProvider: restoreProvider{proof: ProviderProof{
		App: offer.App, Product: offer.Product, BasePlan: offer.BasePlan, Period: oldPeriod, Region: "BE",
	}}, replacement: PlayReplacement{Environment: Sandbox, StoreApp: offer.App, Product: offer.Product, Region: "BE",
		Binding: a.Binding, Previous: oldPeriod.Subscription, Current: newPeriod.Subscription, PreviousInterval: Monthly,
		CurrentInterval: Annual, StoreExpiry: newExpiry, StartedAt: now.Add(time.Minute), ObservedAt: seen}}
	i, err := NewPlayInbox(j, Sandbox, []Offer{offer, next}, fake, bytes.Repeat([]byte{19}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.ReconcileCheckout(ctx, owner, a, offer, previous, nil); err != nil {
		t.Fatal(err)
	}
	if err := i.StageReplacement(ctx, owner, successor, nil); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("longer expiry alone could attach the successor", err)
	}
	fake.linked = ProviderProof{App: next.App, Product: next.Product, BasePlan: next.BasePlan, Region: "BE", Period: newPeriod}
	fake.linked.Period.StartsAt = cutoff.Add(-time.Minute)
	if err := i.StageReplacement(ctx, owner, successor, nil); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("overlapping paid order could attach the successor", err)
	}
	fake.linked.Period = newPeriod
	if err := i.StageReplacement(ctx, owner, successor, nil); err != nil {
		t.Fatal("funded successor could not be discovered after renewal", err)
	}
	var effective int64
	if err := db.QueryRowContext(ctx, "SELECT effective_ns FROM billing_play_replacements").Scan(&effective); err != nil || effective != timeNS(cutoff) {
		t.Fatal("late discovery moved the old paid-through boundary", err)
	}
	if p, err := j.Active(ctx, owner, Sandbox, seen); err != nil || p != nil {
		t.Fatal("staging alone granted a paid period", err)
	}
	var acknowledgements int
	r := PlayReconciler{Inbox: i, Now: func() time.Time { return seen },
		WithLiveOwner: func(_ context.Context, who string, apply func() error) error {
			if who != owner {
				t.Fatal("recovered an unrelated account")
			}
			return apply()
		},
		Acknowledge:            func(context.Context, string, string) error { return ErrUnresolved },
		AcknowledgeReplacement: func(context.Context, string, string, string) error { acknowledgements++; return nil }}
	if batch, err := r.PollKnown(ctx); err != nil || batch.Processed != 1 || acknowledgements != 1 {
		t.Fatal("late paid replacement did not reconcile", batch, err)
	}
	if p, err := j.Active(ctx, owner, Sandbox, seen); err != nil || p == nil || p.Subscription != oldPeriod.Subscription || p.Transaction != newPeriod.Transaction {
		t.Fatal("the rightful owner did not recover the newly paid period", err)
	}
}
