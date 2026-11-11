package email

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

func TestSignedWebhookMatrix(t *testing.T) {
	q, now := testOutbox(t)
	ctx := context.Background()
	key := []byte("webhook-secret-canary-000000000000")
	handler := q.Webhook(key)
	send := func(id, body string, malformed bool) int {
		t.Helper()
		stamp := strconv.FormatInt(now.Unix(), 10)
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write([]byte(id + "." + stamp + "." + body))
		signature := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
		if malformed {
			signature = "v1,not-base64!"
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/email", strings.NewReader(body))
		r.Header.Set("svix-id", id)
		r.Header.Set("svix-timestamp", stamp)
		r.Header.Set("svix-signature", signature)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Body.Len() != 0 {
			t.Fatal("webhook returned a response body")
		}
		return w.Code
	}
	kinds := []string{"failed", "delivered", "suppressed", "bounced", "complained"}
	for i, first := range kinds {
		for j, second := range kinds {
			t.Run(first+"/"+second, func(t *testing.T) {
				provider := fmt.Sprintf("provider-%d-%d", i, j)
				id := enqueueTestMail(t, q, provider)
				w := Worker{Queue: q, sender: &stubSender{result: deliveryResult{id: provider}}, FingerprintKey: make([]byte, 32)}
				if worked, err := w.Step(ctx); !worked || err != nil {
					t.Fatalf("send: %t %v", worked, err)
				}
				for n, kind := range []string{"delivery_delayed", first, second, "delivery_delayed"} {
					body := fmt.Sprintf(`{"type":"email.%s","data":{"email_id":%q,"to":["recipient-canary@example.test"]}}`, kind, provider)
					if code := send(fmt.Sprintf("%s-%d", provider, n), body, false); code != 204 {
						t.Fatalf("event status %d", code)
					}
				}
				var row outbox
				if err := q.Store.DB.First(&row, id).Error; err != nil || row.Outcome != kinds[max(i, j)] || row.State != "accepted" {
					t.Fatalf("outcome=%s state=%s err=%v", row.Outcome, row.State, err)
				}
			})
		}
	}
	body := `{"type":"email.complained","data":{"email_id":"provider-0-0"}}`
	if send("bad-signature", body, true) != 400 || send("oversized", body+strings.Repeat(" ", 64<<10), false) != 400 {
		t.Fatal("invalid request accepted")
	}
	var count int64
	if err := q.Store.DB.Model(&emailEvent{}).Count(&count).Error; err != nil || count != 100 {
		t.Fatalf("rejected requests changed events: %d %v", count, err)
	}
	var row outbox
	if err := q.Store.DB.Where("provider_id = ?", "provider-0-0").First(&row).Error; err != nil || row.Outcome != "failed" {
		t.Fatalf("rejected requests changed outcome: %s %v", row.Outcome, err)
	}
}

func TestEarlyWebhook(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	s, err := store.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	q := Queue{Store: s, Now: func() time.Time { return now }}
	key := []byte("fake-webhook-signing-key-for-test")
	h := q.Webhook(key)
	send := func(id, kind string, age time.Duration, signed bool) int {
		body := `{"type":"email.` + kind + `","data":{"email_id":"test-provider-id","to":["private-canary@example.test"]}}`
		stamp := strconv.FormatInt(now.Add(-age).Unix(), 10)
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(id + "." + stamp + "." + body))
		r := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/email", strings.NewReader(body))
		r.Header.Set("svix-id", id)
		r.Header.Set("svix-timestamp", stamp)
		if signed {
			r.Header.Set("svix-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), "private-canary") {
			t.Fatal("webhook leaked recipient")
		}
		return w.Code
	}
	if send("invalid", "complained", 0, false) != 400 || send("stale", "complained", 10*time.Minute, true) != 400 {
		t.Fatal("accepted invalid event")
	}
	first := send("early", "bounced", 0, true)
	replay := send("early", "bounced", 0, true)
	if first != 204 || replay != 204 {
		t.Fatal("early event or replay rejected")
	}
	var events int
	if err := s.SQL.QueryRow("SELECT count(*) FROM email_events").Scan(&events); err != nil || events != 1 {
		t.Fatalf("event count: %d, %v", events, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	q.Store = reopened
	h = q.Webhook(key)
	id, err := q.Enqueue(ctx, nil, Request{Key: "event", Recipient: "delivered@resend.dev", Template: "provider-smoke-test/v1", Fields: map[string]string{"testID": "test", "timestamp": now.Format(time.RFC3339)}})
	if err != nil {
		t.Fatal(err)
	}
	w := Worker{Queue: q, sender: &stubSender{result: deliveryResult{id: "test-provider-id"}}, FingerprintKey: make([]byte, 32)}
	if _, err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if send("late", "delivered", 0, true) != 204 {
		t.Fatal("delivery event rejected")
	}
	var row outbox
	if err := reopened.DB.First(&row, id).Error; err != nil || row.State != "accepted" || row.Outcome != "bounced" {
		t.Fatalf("reconciliation: %v %s/%s", err, row.State, row.Outcome)
	}
}
