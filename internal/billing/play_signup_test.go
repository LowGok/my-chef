package billing

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestPlaySignupReleasesOnlyKnownProviderCancelledPurchase(t *testing.T) {
	for _, scenario := range []string{"cancelled", "missing pending proof", "other pending token", "wrong environment", "linked purchase", "paid order", "deleted owner"} {
		t.Run(scenario, func(t *testing.T) {
			db, journal, now := fixture(t)
			ctx := t.Context()
			attempt, err := journal.Reserve(ctx, owner, Sandbox, Play, Monthly, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.Launch(ctx, attempt, now); err != nil {
				t.Fatal(err)
			}
			const token = "play-pending-fixture-token"
			const other = "different-pending-fixture-token"
			const app = "com.example.fixture"
			const pendingDeletion = "fixture-pending-play-deletion"
			state := "SUBSCRIPTION_STATE_PENDING"
			extra, order := `,"testPurchase":{}`, ""
			provider, err := NewPlayProvider(PlayConfig{Package: app, Product: "premium", MonthlyBasePlan: "monthly", AnnualBasePlan: "annual", Environment: Sandbox,
				AccessToken: func(context.Context) (string, error) { return "offline-fixture-access", nil }}, &http.Client{Transport: playTestTransport(func(r *http.Request) (*http.Response, error) {
				want := "/androidpublisher/v3/applications/" + app + "/purchases/subscriptionsv2/tokens/"
				if r.URL.Host != "androidpublisher.googleapis.com" || (r.URL.Path != want+token && r.URL.Path != want+other) {
					t.Fatalf("unexpected Play lookup: %s", r.URL.Path)
				}
				return playTestReply(200, fmt.Sprintf(`{"kind":"androidpublisher#subscriptionPurchaseV2","regionCode":"BE","subscriptionState":%q%s,
"externalAccountIdentifiers":{"obfuscatedExternalAccountId":%q},"lineItems":[{"productId":"premium","autoRenewingPlan":{},"offerDetails":{"basePlanId":"monthly"}%s}]}`,
					state, extra, attempt.Binding, order)), nil
			})}, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			offer := Offer{Store: Play, Environment: Sandbox, App: app, Product: "premium", BasePlan: "monthly", Interval: Monthly}
			key := bytes.Repeat([]byte{1}, 32)
			inbox, err := NewPlayInbox(journal, Sandbox, []Offer{offer}, provider, key)
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "missing pending proof" {
				result, err := inbox.ReconcileCheckout(ctx, owner, attempt, offer, token, nil)
				if err != nil || result.CompleteStoreTransaction {
					t.Fatalf("a verified pending purchase should be recorded without a grant, got %+v, %v", result, err)
				}
			}
			if scenario == "other pending token" {
				if result, err := inbox.ReconcileCheckout(ctx, owner, attempt, offer, other, nil); err != nil || result.CompleteStoreTransaction {
					t.Fatalf("a second pending token did not stay locked: %+v, %v", result, err)
				}
			}
			if _, err := journal.Reserve(ctx, owner, Sandbox, Apple, Monthly, now.Add(time.Hour)); !errors.Is(err, ErrCheckoutBusy) {
				t.Fatalf("pending Play purchase permitted new Apple checkout: %v", err)
			}
			current, err := StartRuntime(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := NewPlayInbox(current, Sandbox, []Offer{offer}, provider, key)
			if err != nil {
				t.Fatal(err)
			}
			state = "SUBSCRIPTION_STATE_PENDING_PURCHASE_CANCELED"
			switch scenario {
			case "wrong environment":
				extra = ""
			case "linked purchase":
				extra += `,"linkedPurchaseToken":"previous-play-fixture-token"`
			case "paid order":
				order = `,"latestSuccessfulOrderId":"GPA.1234-5678-9012-34567"`
			case "deleted owner":
				if _, err := db.ExecContext(ctx, "ALTER TABLE lifecycle_deletions ADD COLUMN operation TEXT"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, "CREATE UNIQUE INDEX fixture_lifecycle_operation ON lifecycle_deletions(operation)"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, "INSERT INTO lifecycle_deletions(incarnation,state,operation) VALUES(?,'prepared',?)", owner, pendingDeletion); err != nil {
					t.Fatal(err)
				}
			}
			notice := PlayNotice{ID: "cancelled-play-fixture", Token: token}
			if err := inbox.Retain(ctx, notice, now); !errors.Is(err, ErrUnresolved) {
				t.Fatalf("obsolete generation changed checkout: %v", err)
			}
			err = reopened.Retain(ctx, notice, now)
			var saved string
			if queryErr := db.QueryRowContext(ctx, "SELECT state FROM billing_attempts WHERE id=?", attempt.ID).Scan(&saved); queryErr != nil {
				t.Fatal(queryErr)
			}
			if scenario != "cancelled" {
				if saved != "launched" {
					t.Fatalf("ambiguous outcome released the checkout: state %s, error %v", saved, err)
				}
				if scenario == "deleted owner" {
					if err := transaction(ctx, db, func(tx *sql.Tx) error {
						return PurgeForDeletion(ctx, tx, pendingDeletion, owner)
					}); err != nil {
						t.Fatal(err)
					}
					var count int
					if err := db.QueryRowContext(ctx, "SELECT count(*) FROM billing_play_signups").Scan(&count); err != nil || count != 0 {
						t.Fatalf("deleted account left a pending purchase reference: %d, %v", count, err)
					}
				}
				return
			}
			if err != nil || saved != "cancelled" {
				t.Fatalf("the exact known terminal purchase did not release checkout: state %s, error %v", saved, err)
			}
			if _, err := current.Reserve(ctx, owner, Sandbox, Apple, Monthly, now.Add(time.Hour)); err != nil {
				t.Fatal("could not start a new checkout after verified cancellation", err)
			}
			if err := reopened.Retain(ctx, notice, now); err != nil {
				t.Fatal("redelivered cancellation was not idempotent", err)
			}
			if period, err := current.Active(ctx, owner, Sandbox, now); err != nil || period != nil {
				t.Fatalf("unpaid terminal purchase granted entitlement: %v", err)
			}
		})
	}
}

func TestPlayKnownPendingRecoversWithoutNotification(t *testing.T) {
	for _, state := range []string{"SUBSCRIPTION_STATE_ACTIVE", "SUBSCRIPTION_STATE_PENDING_PURCHASE_CANCELED"} {
		t.Run(state, func(t *testing.T) {
			db, journal, now := fixture(t)
			ctx := t.Context()
			attempt, err := journal.Reserve(ctx, owner, Sandbox, Play, Monthly, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.Launch(ctx, attempt, now); err != nil {
				t.Fatal(err)
			}
			const app, product = "com.example.fixture", "premium"
			const token, order = "offline-pending-recovery-token", "GPA.1111-2222-3333-44444"
			currentState := "SUBSCRIPTION_STATE_PENDING"
			clock := now
			start, end := now.Add(time.Minute), now.AddDate(0, 1, 0)
			provider, err := NewPlayProvider(PlayConfig{Package: app, Product: product, MonthlyBasePlan: "monthly", AnnualBasePlan: "annual", Environment: Sandbox,
				AccessToken: func(context.Context) (string, error) { return "offline-fixture-access", nil }}, &http.Client{Transport: playTestTransport(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/androidpublisher/v3/applications/" + app + "/purchases/subscriptionsv2/tokens/" + token:
					extra := ""
					if currentState == "SUBSCRIPTION_STATE_ACTIVE" {
						extra = fmt.Sprintf(`,"latestSuccessfulOrderId":%q,"expiryTime":%q,"offerPhase":{"basePrice":{}}`, order, end.Format(time.RFC3339))
					}
					return playTestReply(200, fmt.Sprintf(`{"kind":"androidpublisher#subscriptionPurchaseV2","regionCode":"BE","subscriptionState":%q,"testPurchase":{},
 "externalAccountIdentifiers":{"obfuscatedExternalAccountId":%q},"lineItems":[{"productId":"premium","autoRenewingPlan":{},"offerDetails":{"basePlanId":"monthly"}%s}]}`,
						currentState, attempt.Binding, extra)), nil
				case "/androidpublisher/v3/applications/" + app + "/orders/" + order:
					return playTestReply(200, fmt.Sprintf(`{"orderId":%q,"purchaseToken":%q,"state":"PROCESSED","total":{"currencyCode":"EUR","units":"6","nanos":990000000},
 "lineItems":[{"productId":"premium","total":{"currencyCode":"EUR","units":"6","nanos":990000000},"subscriptionDetails":{"basePlanId":"monthly","offerPhaseDetails":{"baseDetails":{}},"servicePeriodStartTime":%q,"servicePeriodEndTime":%q}}]}`,
						order, token, start.Format(time.RFC3339), end.Format(time.RFC3339))), nil
				default:
					t.Fatalf("unexpected Play lookup: %s", r.URL.Path)
					return nil, ErrStoreUnavailable
				}
			})}, func() time.Time { return clock })
			if err != nil {
				t.Fatal(err)
			}
			offer := Offer{Store: Play, Environment: Sandbox, App: app, Product: product, BasePlan: "monthly", Interval: Monthly}
			key := bytes.Repeat([]byte{2}, 32)
			inbox, err := NewPlayInbox(journal, Sandbox, []Offer{offer}, provider, key)
			if err != nil {
				t.Fatal(err)
			}
			if result, err := inbox.ReconcileCheckout(ctx, owner, attempt, offer, token, nil); err != nil || result.CompleteStoreTransaction {
				t.Fatalf("could not journal pending purchase without acknowledging: %+v, %v", result, err)
			}
			var ciphertext []byte
			if err := db.QueryRowContext(ctx, `SELECT ciphertext FROM billing_play_polls WHERE binding=?`, attempt.Binding).Scan(&ciphertext); err != nil || bytes.Contains(ciphertext, []byte(token)) {
				t.Fatal("no encrypted retry reference", err)
			}
			journal, err = StartRuntime(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			inbox, err = NewPlayInbox(journal, Sandbox, []Offer{offer}, provider, key)
			if err != nil {
				t.Fatal(err)
			}
			clock = now.Add(time.Hour)
			currentState = state
			var acks int
			worker := PlayReconciler{Inbox: inbox, Now: func() time.Time { return clock }, WithLiveOwner: func(_ context.Context, found string, commit func() error) error {
				if found != owner {
					t.Fatal("incorrect live owner")
				}
				return commit()
			}, Acknowledge: func(ctx context.Context, value, binding string) error {
				acks++
				period, err := journal.Active(ctx, owner, Sandbox, clock)
				if value != token || binding != attempt.Binding || err != nil || period == nil || period.Transaction != order {
					t.Fatalf("acknowledged before correct durable grant: %v", err)
				}
				return nil
			}}
			if batch, err := worker.PollKnown(ctx); err != nil || batch.Processed != 1 || batch.Failed != 0 {
				t.Fatalf("pending purchase was not recovered from the retry worker: %+v, %v", batch, err)
			}
			if state == "SUBSCRIPTION_STATE_ACTIVE" {
				if acks != 1 {
					t.Fatalf("paid purchase acknowledgements: %d", acks)
				}
			} else {
				if acks != 0 {
					t.Fatal("acknowledged a cancelled purchase")
				}
				if _, err := journal.Reserve(ctx, owner, Sandbox, Apple, Annual, clock); err != nil {
					t.Fatal("did not reopen checkout", err)
				}
				var count int
				if err := db.QueryRowContext(ctx, `SELECT count(*) FROM billing_play_polls WHERE binding=?`, attempt.Binding).Scan(&count); err != nil || count != 0 {
					t.Fatal("retained the encrypted token after terminal cancellation", err)
				}
			}
		})
	}
}
