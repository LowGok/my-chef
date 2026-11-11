package email

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

type stubSender struct {
	binding string
	calls   int
	result  deliveryResult
}

type blockedSender struct{ started, release chan struct{} }

func (s blockedSender) bindingID() string { return "blocked-test" }
func (s blockedSender) send(ctx context.Context, _ string, _ []byte) deliveryResult {
	close(s.started)
	select {
	case <-s.release:
		return deliveryResult{id: "fake-accepted"}
	case <-ctx.Done():
		return deliveryResult{temporary: true}
	}
}

func TestEnqueueWhileProviderIsBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := store.Open(filepath.Join(t.TempDir(), "db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	q := Queue{Store: s}
	request := Request{Key: "first", Recipient: "delivered@resend.dev", Template: "provider-smoke-test/v1", Fields: map[string]string{"testID": "test", "timestamp": "2026-11-06T12:00:00Z"}}
	if _, err := q.Enqueue(ctx, nil, request); err != nil {
		t.Fatal(err)
	}
	p := blockedSender{started: make(chan struct{}), release: make(chan struct{})}
	w := Worker{Queue: q, sender: p, FingerprintKey: make([]byte, 32)}
	done := make(chan error, 1)
	go func() { _, err := w.Step(ctx); done <- err }()
	select {
	case <-p.started:
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	request.Key = "second"
	id, enqueueErr := q.Enqueue(ctx, nil, request)
	close(p.release)
	if workerErr := <-done; workerErr != nil {
		t.Fatal(workerErr)
	}
	if enqueueErr != nil || id == 0 {
		t.Fatalf("enqueue during blocked delivery: %v", enqueueErr)
	}
}

func (s *stubSender) send(context.Context, string, []byte) deliveryResult { s.calls++; return s.result }
func (s *stubSender) bindingID() string                                   { return s.binding }

func TestRetryBinding(t *testing.T) {
	for _, change := range []string{"none", "sender", "reply-to", "credential", "key unavailable"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			s, err := store.Open(filepath.Join(t.TempDir(), "db"), true)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if err := s.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
			q := Queue{Store: s, Now: func() time.Time { return now }}
			id, err := q.Enqueue(ctx, nil, Request{Key: "test", Recipient: "delivered@resend.dev", Template: "provider-smoke-test/v1", Fields: map[string]string{"testID": "test", "timestamp": now.Format(time.RFC3339)}})
			if err != nil {
				t.Fatal(err)
			}
			p := &stubSender{binding: "credential", result: deliveryResult{temporary: true}}
			w := Worker{Queue: q, sender: p, From: "from@example.test", FingerprintKey: make([]byte, 32), Random: func() float64 { return .5 }}
			if _, err := w.Step(ctx); err != nil {
				t.Fatal(err)
			}
			if p.calls != 1 {
				t.Fatalf("calls: %d", p.calls)
			}
			now = now.Add(30 * time.Second)
			switch change {
			case "sender":
				w.From = "changed@example.test"
			case "reply-to":
				w.ReplyTo = "changed@example.test"
			case "credential":
				p.binding = "changed"
			case "key unavailable":
				w.FingerprintKey = nil
			}
			p.result = deliveryResult{id: "provider-1"}
			if _, err := w.Step(ctx); err != nil {
				t.Fatal(err)
			}
			var row outbox
			if err := s.DB.First(&row, id).Error; err != nil {
				t.Fatal(err)
			}
			if change == "none" {
				if row.State != "accepted" || p.calls != 2 {
					t.Fatal("unchanged request did not retry")
				}
			} else if row.State != "failed" || row.Reason != "configuration_changed" || p.calls != 1 {
				t.Fatal("changed request was not stopped safely")
			}
		})
	}
}
