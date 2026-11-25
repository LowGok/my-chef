package billing

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAppleCallbackAcknowledgesOnlyVerifiedAndRetainedWork(t *testing.T) {
	db, j, now := fixture(t)
	ctx := t.Context()
	a, err := j.Reserve(ctx, owner, Sandbox, Apple, Monthly, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Launch(ctx, a, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	roots, chain, key := appleChainFixture(t, now, true)
	_, _, otherKey := appleChainFixture(t, now, false)
	p, err := NewAppleProvider(AppleConfig{BundleID: "com.example.preview", GroupID: "group-1", Environment: Sandbox,
		Issuer: "00000000-0000-0000-0000-000000000001", KeyID: "ABCDEFGHIJ", Key: key, Roots: roots,
		Products: map[string]Interval{"premium-monthly": Monthly, "premium-yearly": Annual}},
		&http.Client{Transport: appleTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("callback must retain work before fetching authoritative store state")
			return nil, nil
		})}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	handler := p.NotificationHandler(j)
	payload := func(ref, binding, env string, wrongSignature bool) string {
		header := map[string]any{"alg": "ES256", "x5c": chain}
		tx := map[string]any{"bundleId": "com.example.preview", "environment": env, "appAccountToken": binding,
			"transactionId": ref, "productId": "premium-monthly", "subscriptionGroupIdentifier": "group-1",
			"type": "Auto-Renewable Subscription", "signedDate": now.UnixMilli()}
		signer := key
		if wrongSignature {
			signer = otherKey
		}
		inner := appleSignFixture(t, header, tx, signer)
		signed := appleSignFixture(t, header, map[string]any{
			"notificationUUID": "99887766-5544-4321-8123-4567890abc01", "notificationType": "SUBSCRIBED",
			"version": "2.0", "signedDate": now.UnixMilli(), "data": map[string]any{
				"bundleId": "com.example.preview", "environment": env, "signedTransactionInfo": inner,
			}}, key)
		raw, err := json.Marshal(map[string]string{"signedPayload": signed})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	expect := func(method, media, body string, code, queued int) {
		t.Helper()
		req := httptest.NewRequest(method, "/fixture/apple-notification", strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", media)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != code || rec.Body.Len() != 0 {
			t.Fatalf("unexpected callback response: status %d, body length %d", rec.Code, rec.Body.Len())
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM billing_apple_notices").Scan(&count); err != nil || count != queued {
			t.Fatal("unexpected journal depth", count, err)
		}
	}
	const media = "application/json"
	valid := payload("123", a.Binding, "Sandbox", false)
	expect(http.MethodGet, media, valid, 405, 0)
	expect(http.MethodPost, "text/plain", valid, 415, 0)
	expect(http.MethodPost, media, strings.Repeat("x", 65<<10), 413, 0)
	expect(http.MethodPost, media, valid+"{}", 400, 0)
	expect(http.MethodPost, media, `{"signedPayload":"not signed"}`, 400, 0)
	expect(http.MethodPost, media, payload("123", a.Binding, "Sandbox", true), 400, 0)
	expect(http.MethodPost, media, payload("123", a.Binding, "Production", false), 400, 0)
	expect(http.MethodPost, media, payload("123", "99887766-5544-4321-8123-4567890abc02", "Sandbox", false), 204, 0)

	if _, err := db.ExecContext(ctx, `CREATE TRIGGER fixture_failed_notice BEFORE INSERT ON billing_apple_notices
		BEGIN SELECT RAISE(ABORT, 'fixture write failure'); END`); err != nil {
		t.Fatal(err)
	}
	expect(http.MethodPost, media, valid, 503, 0)
	if _, err := db.ExecContext(ctx, `DROP TRIGGER fixture_failed_notice`); err != nil {
		t.Fatal(err)
	}
	expect(http.MethodPost, media+"; charset=utf-8", valid, 204, 1)
	handler = p.NotificationHandler(NewJournal(db))
	expect(http.MethodPost, media, valid, 204, 1)
	expect(http.MethodPost, media, payload("124", a.Binding, "Sandbox", false), 400, 1)
	if period, err := j.Active(ctx, owner, Sandbox, now); err != nil || period != nil {
		t.Fatal("callback granted Premium without reading current Apple state", err)
	}
}
