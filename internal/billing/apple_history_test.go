package billing

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAppleHistoryResumesWithoutTrustingStoredNotificationState(t *testing.T) {
	db, j, now := fixture(t)
	ctx := t.Context()
	a, err := j.Reserve(ctx, owner, Sandbox, Apple, Monthly, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Launch(ctx, a, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	roots, chain, signer := appleChainFixture(t, now, true)
	_, _, otherKey := appleChainFixture(t, now, false)
	header := map[string]any{"alg": "ES256", "x5c": chain}
	firstID := "99887766-5544-4321-8123-4567890abc01"
	secondID := "99887766-5544-4321-8123-4567890abc02"
	notice := func(event, token, ref string, tamperInner bool) string {
		tx := map[string]any{"bundleId": "com.example.preview", "environment": "Sandbox", "appAccountToken": token,
			"transactionId": ref, "productId": "premium-monthly", "subscriptionGroupIdentifier": "group-1",
			"type": "Auto-Renewable Subscription", "signedDate": now.Add(-2 * time.Minute).UnixMilli()}
		key := signer
		if tamperInner {
			key = otherKey
		}
		return appleSignFixture(t, header, map[string]any{"notificationUUID": event, "notificationType": "SUBSCRIBED",
			"version": "2.0", "signedDate": now.Add(-2 * time.Minute).UnixMilli(), "data": map[string]any{
				"bundleId": "com.example.preview", "environment": "Sandbox", "signedTransactionInfo": appleSignFixture(t, header, tx, key),
			}}, signer)
	}
	var tokens []string
	var requested [][2]int64
	blocked := true
	client := &http.Client{Transport: appleTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Host != "api.storekit-sandbox.apple.com" || req.URL.Path != "/inApps/v1/notifications/history" ||
			!strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") || req.Header.Get("Content-Type") != "application/json" {
			t.Fatal("history request used the wrong origin or lost authentication")
		}
		var query struct {
			Start int64 `json:"startDate"`
			End   int64 `json:"endDate"`
		}
		if json.NewDecoder(req.Body).Decode(&query) != nil || query.End <= query.Start || query.End > now.UnixMilli() {
			t.Fatal("invalid explicit history window")
		}
		requested = append(requested, [2]int64{query.Start, query.End})
		page := req.URL.Query().Get("paginationToken")
		tokens = append(tokens, page)
		payloads := []string{notice(firstID, a.Binding, "123", false)}
		more, next := true, "next/page+1"
		if page != "" {
			if page != next {
				t.Fatal("opaque token changed")
			}
			payloads = []string{notice(firstID, a.Binding, "123", false), notice(secondID, a.Binding, "124", blocked),
				notice("99887766-5544-4321-8123-4567890abc03", "99887766-5544-4321-8123-4567890abc04", "125", false)}
			more, next = false, ""
		}
		items := make([]map[string]string, 0, len(payloads))
		for _, p := range payloads {
			items = append(items, map[string]string{"signedPayload": p})
		}
		body, _ := json.Marshal(map[string]any{"notificationHistory": items, "hasMore": more, "paginationToken": next})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	p, err := NewAppleProvider(AppleConfig{BundleID: "com.example.preview", GroupID: "group-1", Environment: Sandbox,
		Issuer: "00000000-0000-0000-0000-000000000001", KeyID: "ABCDEFGHIJ", Key: otherKey, Roots: roots,
		Products: map[string]Interval{"premium-monthly": Monthly, "premium-yearly": Annual}}, client, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err = p.SyncNotificationHistory(ctx, j); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("history accepted tampered nested transaction", err)
	}
	assertCount := func(want int) {
		t.Helper()
		var got int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM billing_apple_notices").Scan(&got); err != nil || got != want {
			t.Fatal("unexpected queued events", got, err)
		}
	}
	assertCount(1)
	blocked = false
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER billing_fixture_disk_error BEFORE INSERT ON billing_apple_notices
 WHEN NEW.transaction_id='124' BEGIN SELECT RAISE(ABORT, 'fixture write error'); END`); err != nil {
		t.Fatal(err)
	}
	if err := p.SyncNotificationHistory(ctx, NewJournal(db)); err == nil {
		t.Fatal("history acknowledged a page whose work was not persisted")
	}
	assertCount(1)
	if _, err := db.ExecContext(ctx, `DROP TRIGGER billing_fixture_disk_error`); err != nil {
		t.Fatal(err)
	}
	if err = p.SyncNotificationHistory(ctx, NewJournal(db)); err != nil {
		t.Fatal(err)
	}
	assertCount(2)
	if len(tokens) != 4 || tokens[0] != "" || tokens[1] != "next/page+1" || tokens[2] != tokens[1] || tokens[3] != tokens[1] ||
		requested[0] != requested[1] || requested[1] != requested[2] || requested[2] != requested[3] {
		t.Fatal("restart skipped the failed page or changed its original request window", tokens, requested)
	}
	if err = p.SyncNotificationHistory(ctx, j); err != nil || len(tokens) != 4 {
		t.Fatal("unchanged clock started an already checked history window", err)
	}
	if period, err := j.Active(ctx, owner, Sandbox, now); err != nil || period != nil {
		t.Fatal("historical notice granted Premium without reading current Apple state", err)
	}
}
