package email

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testOutbox(t *testing.T) (Queue, *time.Time) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "outbox.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	return Queue{Store: s, Now: func() time.Time { return now }}, &now
}

func enqueueTestMail(t *testing.T, q Queue, key string) int64 {
	t.Helper()
	id, err := q.Enqueue(context.Background(), nil, Request{Key: key, Recipient: "recipient-canary@example.test", Template: "provider-smoke-test/v1", Fields: map[string]string{"testID": "message-canary", "timestamp": "2026-11-06T12:00:00Z"}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestResendLostResponseAcrossRestart(t *testing.T) {
	for _, change := range []string{"unchanged", "sender", "reply-to", "credential", "template", "missing-key"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			q, now := testOutbox(t)
			id := enqueueTestMail(t, q, "retry/same")
			var mu sync.Mutex
			requests := map[string]string{}
			calls, acceptances := 0, 0
			client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				defer func() { _ = r.Body.Close() }()
				body, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				mu.Lock()
				defer mu.Unlock()
				calls++
				key := r.Header.Get("Idempotency-Key")
				reply := func(status int, body string) (*http.Response, error) {
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
				}
				if key != "retry/same" || r.Header.Get("Authorization") != "Bearer credential-canary" {
					return reply(400, "{}")
				}
				if previous, ok := requests[key]; ok {
					if previous != string(body) {
						return reply(409, "{}")
					}
				} else {
					requests[key] = string(body)
					acceptances++
				}
				if calls == 1 {
					// Accept the request, but lose the response.
					return nil, io.ErrUnexpectedEOF
				}
				return reply(200, `{"id":"one-acceptance"}`)
			})}
			fp := make([]byte, 32)
			sender := resend{endpoint: "https://resend.invalid/emails", key: "credential-canary", client: client}
			w := Worker{Queue: q, sender: sender, From: "sender@example.test", FingerprintKey: fp, Random: func() float64 { return .5 }}
			// Simulate process death between provider submission and persisting its result.
			if _, err := q.Store.SQL.Exec("CREATE TRIGGER lose_result BEFORE UPDATE ON email_outbox WHEN OLD.state='sending' BEGIN SELECT RAISE(ABORT, 'lost result'); END"); err != nil {
				t.Fatal(err)
			}
			if worked, err := w.Step(ctx); !worked || err != errStorage {
				t.Fatalf("interrupted step: %t %v", worked, err)
			}
			if err := q.Store.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := store.Open(q.Store.Path, false)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			q.Store = s
			if _, err := s.SQL.Exec("DROP TRIGGER lose_result"); err != nil {
				t.Fatal(err)
			}
			var row outbox
			if err := s.DB.First(&row, id).Error; err != nil || row.State != "sending" {
				t.Fatalf("recoverable claim: %s %v", row.State, err)
			}
			*now = now.Add(30 * time.Second)
			retry := Worker{Queue: q, sender: sender, From: "sender@example.test", FingerprintKey: fp}
			switch change {
			case "sender":
				retry.From = "other@example.test"
			case "reply-to":
				retry.ReplyTo = "other@example.test"
			case "credential":
				sender.key = "different"
				retry.sender = sender
			case "template":
				if err := s.DB.Model(&row).Update("data", `{"testID":"changed-rendering","timestamp":"2026-11-06T12:00:00Z"}`).Error; err != nil {
					t.Fatal(err)
				}
			case "missing-key":
				retry.FingerprintKey = nil
			}
			if worked, err := retry.Step(ctx); !worked || err != nil {
				t.Fatalf("retry step: %t %v", worked, err)
			}
			if err := s.DB.First(&row, id).Error; err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if acceptances != 1 || len(requests) != 1 {
				t.Fatal("duplicate provider acceptance")
			}
			if change == "unchanged" {
				if calls != 2 || row.State != "accepted" {
					t.Fatalf("retry: calls=%d state=%s", calls, row.State)
				}
			} else if calls != 1 || row.State != "failed" || row.Reason != "configuration_changed" {
				t.Fatalf("unsafe retry: calls=%d state=%s reason=%s", calls, row.State, row.Reason)
			}
		})
	}
}

func TestTerminalFailureLeavesQueueMoving(t *testing.T) {
	for _, tc := range []struct {
		name, reason, outcome string
		result                deliveryResult
		attempts              int
		age                   time.Duration
		calls                 int
	}{
		{"permanent", "provider_rejected", "", deliveryResult{}, 0, 0, 1},
		{"suppression", "provider_suppressed", "suppressed", deliveryResult{suppressed: true}, 0, 0, 1},
		{"attempt-limit", "retry_exhausted", "", deliveryResult{temporary: true}, 16, 0, 0},
		{"time-limit", "retry_exhausted", "", deliveryResult{temporary: true}, 0, 23 * time.Hour, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			q, now := testOutbox(t)
			first := enqueueTestMail(t, q, "first")
			if err := q.Store.DB.Model(&outbox{}).Where("id = ?", first).Updates(map[string]any{"attempts": tc.attempts, "created_at": now.Add(-tc.age).Unix()}).Error; err != nil {
				t.Fatal(err)
			}
			second := enqueueTestMail(t, q, "second")
			p := &stubSender{result: tc.result}
			w := Worker{Queue: q, sender: p, FingerprintKey: make([]byte, 32)}
			if _, err := w.Step(ctx); err != nil {
				t.Fatal(err)
			}
			var row outbox
			if err := q.Store.DB.First(&row, first).Error; err != nil || row.State != "failed" || row.Reason != tc.reason || row.Outcome != tc.outcome || p.calls != tc.calls {
				t.Fatalf("terminal result: %s/%s/%s calls=%d err=%v", row.State, row.Reason, row.Outcome, p.calls, err)
			}
			p.result = deliveryResult{id: "next"}
			if _, err := w.Step(ctx); err != nil {
				t.Fatal(err)
			}
			row = outbox{}
			if err := q.Store.DB.First(&row, second).Error; err != nil || row.State != "accepted" {
				t.Fatalf("next item: %s %v", row.State, err)
			}
		})
	}
}

func TestRetryDelayBounds(t *testing.T) {
	low := Worker{Random: func() float64 { return 0 }}
	high := Worker{Random: func() float64 { return 1 }}
	if low.delay(1) != 24*time.Second || high.delay(1) != 36*time.Second ||
		low.delay(2) != 48*time.Second || high.delay(2) != 72*time.Second || high.delay(16) != 30*time.Minute {
		t.Fatal("retry delays do not match exponential backoff and jitter bounds")
	}
}
