package email

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	httpapi "github.com/LowGok/my-chef/internal/http"
)

type crashingSender struct{}

func (crashingSender) bindingID() string                                   { return "credential-canary" }
func (crashingSender) send(context.Context, string, []byte) deliveryResult { panic("message-canary") }

func TestServiceReadinessAndRedaction(t *testing.T) {
	for _, failure := range []string{"transient", "storage", "worker"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			q, _ := testOutbox(t)
			enqueueTestMail(t, q, "readiness")
			s := New(q, config.Email{Fake: true, From: "sender@example.test"})
			var logs bytes.Buffer
			api := httpapi.New(q.Store, nil, httpapi.Config{EmailReady: s.Ready, EmailWebhook: s.Webhook()}, slog.New(slog.NewJSONHandler(&logs, nil))).Handler(nil)
			check := func(path string, want int) {
				t.Helper()
				w := httptest.NewRecorder()
				api.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
				if w.Code != want {
					t.Fatalf("%s: %d want %d", path, w.Code, want)
				}
				for _, secret := range []string{"credential-canary", "message-canary", "recipient-canary@example.test"} {
					if strings.Contains(w.Body.String(), secret) || strings.Contains(logs.String(), secret) {
						t.Fatal("email diagnostics disclosed a canary")
					}
				}
			}
			check("/readyz", 200)
			switch failure {
			case "transient":
				s.worker.sender = resend{endpoint: "https://resend.invalid/emails", key: "credential-canary", client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{"message":"credential-canary message-canary recipient-canary@example.test"}`))}, nil
				})}}
				if worked, err := s.worker.Step(ctx); !worked || err != nil {
					t.Fatalf("transient result: %t %v", worked, err)
				}
				check("/readyz", 200)
			case "storage":
				if _, err := q.Store.SQL.Exec("DROP TABLE email_outbox"); err != nil {
					t.Fatal(err)
				}
				s.Run(ctx)
				check("/readyz", 503)
			case "worker":
				s.worker.sender = crashingSender{}
				s.Run(ctx)
				check("/readyz", 503)
			}
			check("/healthz", 200)
		})
	}
}

func TestEmailRejectsUnsupportedInputs(t *testing.T) {
	q, _ := testOutbox(t)
	for _, tc := range []struct{ name, recipient, template, field string }{
		{"marketing", "one@example.test", "marketing/v1", ""},
		{"bulk", "one@example.test,two@example.test", "provider-smoke-test/v1", ""},
		{"attachment", "one@example.test", "provider-smoke-test/v1", "attachments"},
		{"inbound", "one@example.test", "inbound/v1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]string{"testID": "test", "timestamp": "2026-11-06T12:00:00Z"}
			if tc.field != "" {
				fields[tc.field] = "message-canary"
			}
			if id, err := q.Enqueue(context.Background(), nil, Request{Key: tc.name, Recipient: tc.recipient, Template: tc.template, Fields: fields}); id != 0 || err == nil {
				t.Fatal("unsupported request was queued")
			}
		})
	}
	var rows int64
	if err := q.Store.DB.Model(&outbox{}).Count(&rows).Error; err != nil || rows != 0 {
		t.Fatalf("rejected input changed outbox: %d %v", rows, err)
	}
}

func TestDisabledServiceKeepsAccountStorageReadyWithoutDispatch(t *testing.T) {
	q, _ := testOutbox(t)
	enqueueTestMail(t, q, "disabled-queued")
	service := New(q, config.Email{Disabled: true})
	service.worker.sender = crashingSender{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { service.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	select {
	case <-done:
		t.Fatal("disabled service tried to run its sender")
	case <-time.After(100 * time.Millisecond):
	}
	if err := service.Smoke(ctx, Request{}); err == nil {
		t.Fatal("disabled service accepted a smoke send")
	}
	if !service.Ready(ctx) || service.Webhook() != nil {
		t.Fatal("disabled service blocked account storage or exposed a webhook")
	}
	var attempts int
	if err := q.Store.SQL.QueryRow("SELECT attempts FROM email_outbox").Scan(&attempts); err != nil || attempts != 0 {
		t.Fatal("disabled service dispatched mail", err)
	}
}
