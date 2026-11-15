package billing

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestPlayWorkerRecoversClosedClientAndRetriesAcknowledgement(t *testing.T) {
	db, j, now := fixture(t)
	ctx := t.Context()
	attempt, err := j.Reserve(ctx, owner, Sandbox, Play, Monthly, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Launch(ctx, attempt, now); err != nil {
		t.Fatal(err)
	}
	const token = "fixture-protected-purchase"
	proof := ProviderProof{App: "com.example.fixture", Product: "premium", BasePlan: "monthly", Region: "BE",
		Period: VerifiedPeriod{Store: Play, Environment: Sandbox, Binding: attempt.Binding, Subscription: playTokenDigest(token),
			Transaction: "GPA.1234-5678-9012-34567", Interval: Monthly, StartsAt: now, PaidThrough: now.Add(time.Hour), ObservedAt: now}}
	offers := []Offer{{Store: Play, Environment: Sandbox, App: proof.App, Product: proof.Product, BasePlan: proof.BasePlan, Interval: Monthly}}
	key := bytes.Repeat([]byte{7}, 32)
	provider := &pollingVerifier{restoreProvider: restoreProvider{proof: proof}}
	inbox, err := NewPlayInbox(j, Sandbox, offers, provider, key)
	if err != nil {
		t.Fatal(err)
	}
	notice := PlayNotice{ID: "event-completed", Token: token}
	if err := inbox.Retain(ctx, notice, now); err != nil {
		t.Fatal(err)
	}

	clock := now
	var acks, fences int
	deliver := func(ctx context.Context, raw, binding string) error {
		acks++
		persisted, err := j.Active(ctx, owner, Sandbox, clock)
		if err != nil || persisted == nil || persisted.Transaction != proof.Period.Transaction || raw != token || binding != attempt.Binding {
			t.Fatal("acknowledgement preceded the original owner's durable purchase", err)
		}
		if acks == 1 {
			return errors.New("provider diagnostics must not escape")
		}
		return nil
	}
	fence := func(_ context.Context, who string, commit func() error) error {
		if who != owner {
			t.Fatal("purchase escaped its original owner")
		}
		fences++
		return commit()
	}
	runner := PlayReconciler{Inbox: inbox, Acknowledge: deliver, WithLiveOwner: fence, Now: func() time.Time { return clock }}
	stale, err := inbox.claim(ctx, clock)
	if err != nil {
		t.Fatal(err)
	}
	clock = now.Add(time.Minute)
	if err := runner.reconcile(ctx, stale, runner.Now); !errors.Is(err, ErrUnresolved) {
		t.Fatal("expired worker could still apply a purchase", err)
	}
	if p, err := j.Active(ctx, owner, Sandbox, clock); err != nil || p != nil || acks != 0 {
		t.Fatal("expired lease changed entitlement or contacted the store", err)
	}
	if batch, err := runner.RunBatch(ctx); err != nil || batch.Processed != 1 || batch.Failed != 0 || acks != 1 {
		t.Fatal("first acknowledgement error was not durably delayed", batch, err)
	}

	reopened, err := NewPlayInbox(NewJournal(db), Sandbox, offers, provider, key)
	if err != nil {
		t.Fatal(err)
	}
	runner.Inbox = reopened
	if batch, err := runner.RunBatch(ctx); err != nil || batch.Processed != 0 || acks != 1 {
		t.Fatal("restart did not preserve the retry delay", batch, err)
	}
	clock = clock.Add(time.Minute)
	if batch, err := runner.RunBatch(ctx); err != nil || batch.Processed != 1 || acks != 2 || fences < 2 {
		t.Fatal("restart lost the original acknowledgement", batch, err)
	}
	if err := reopened.Retain(ctx, notice, clock); err != nil {
		t.Fatal(err)
	}
	if batch, err := runner.RunBatch(ctx); err != nil || batch.Processed != 0 || acks != 2 {
		t.Fatal("a redelivery re-opened completed work", batch, err)
	}
}

func TestPlayAcknowledgeUsesLiveStateAndDoesNotRepeatCompletedPost(t *testing.T) {
	const app = "com.example.fixture"
	const token = "fixture-token"
	const binding = "5095aae8-27e3-45de-bef1-bf9b179cedcf"
	path := "/androidpublisher/v3/applications/" + app + "/purchases/"
	posts, reads := 0, 0
	state := "ACKNOWLEDGEMENT_STATE_PENDING"
	client := &http.Client{Transport: playTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "androidpublisher.googleapis.com" || r.Header.Get("Authorization") != "Bearer offline-token" {
			t.Fatal("wrong Play destination")
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == path+"subscriptionsv2/tokens/"+token:
			reads++
			return playTestReply(200, `{"kind":"androidpublisher#subscriptionPurchaseV2","testPurchase":{},
"regionCode":"BE","subscriptionState":"SUBSCRIPTION_STATE_ACTIVE","acknowledgementState":"`+state+`",
"externalAccountIdentifiers":{"obfuscatedExternalAccountId":"`+binding+`"},
"lineItems":[{"productId":"premium","offerDetails":{"basePlanId":"monthly"},"autoRenewingPlan":{}}]}`), nil
		case r.Method == http.MethodPost && r.URL.Path == path+"subscriptions/premium/tokens/"+token+":acknowledge":
			posts++
			raw, err := io.ReadAll(r.Body)
			if err != nil || string(raw) != "{}" || r.Header.Get("Content-Type") != "application/json" {
				t.Fatal("unexpected acknowledgement body")
			}
			state = "ACKNOWLEDGEMENT_STATE_ACKNOWLEDGED"
			return nil, errors.New("fixture lost response after store accepted it")
		default:
			t.Fatal("unexpected store route")
			return nil, ErrStoreUnavailable
		}
	})}
	provider, err := NewPlayProvider(PlayConfig{Package: app, Product: "premium", MonthlyBasePlan: "monthly", AnnualBasePlan: "annual",
		Environment: Sandbox, AccessToken: func(context.Context) (string, error) { return "offline-token", nil }}, client, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Acknowledge(t.Context(), token, "a972c014-6973-4c34-b36c-7e5a620ce54b"); !errors.Is(err, ErrPurchaseBinding) || posts != 0 {
		t.Fatal("a different account could acknowledge this purchase", err)
	}
	if err := provider.Acknowledge(t.Context(), token, binding); !errors.Is(err, ErrStoreUnavailable) || posts != 1 {
		t.Fatal("a lost response did not stay unresolved for retry", err)
	}
	if err := provider.Acknowledge(t.Context(), token, binding); err != nil || posts != 1 || reads != 3 {
		t.Fatal("retry failed to recognize the completed acknowledgement", err)
	}
}
