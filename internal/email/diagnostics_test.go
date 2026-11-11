package email

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/LowGok/my-chef/internal/config"
	httpapi "github.com/LowGok/my-chef/internal/http"
)

func scanDiagnostics(root string, canaries []string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, c := range canaries {
			if strings.Contains(string(data), c) {
				return fmt.Errorf("canary %d in diagnostic artifact %s", i+1, filepath.Base(path))
			}
		}
		return nil
	})
}

func TestOfflineRetainedEmailDiagnostics(t *testing.T) {
	ctx := context.Background()
	q, now := testOutbox(t)
	root := t.TempDir()
	traceDir := filepath.Join(root, "traces")
	t.Setenv("MYCHEF_TRACE_DIR", traceDir)
	if err := os.Mkdir(traceDir, 0700); err != nil {
		t.Fatal(err)
	}
	const credential = "re_OFFLINE_DIAGNOSTIC_CANARY"
	const signing = "offline-signing-secret-canary-key"
	const recipient = "retained-diagnostic-recipient@example.test"
	const reset = "https://example.test/reset#offline-token-canary"
	const message = "offline-template-data-canary"
	canaries := []string{credential, signing, recipient, reset, message, "MyChef email test"}
	control := t.TempDir()
	if err := os.WriteFile(filepath.Join(control, "synthetic-trace.json"), []byte(reset), 0600); err != nil {
		t.Fatal(err)
	}
	if scanDiagnostics(control, canaries) == nil {
		t.Fatal("scan did not detect the planted reset-link canary")
	}

	logFile, err := os.Create(filepath.Join(root, "http.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()
	capture, err := os.Create(filepath.Join(root, "responses.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = capture.Close() }()
	record := func(label string, value any) {
		t.Helper()
		if _, err := fmt.Fprintf(capture, "%s: %v\n", label, value); err != nil {
			t.Fatal(err)
		}
	}
	s := New(q, config.Email{Fake: true, From: "sender@example.test"})
	handler := httpapi.New(q.Store, nil, httpapi.Config{EmailReady: s.Ready, EmailWebhook: q.Webhook([]byte(signing))}, slog.New(slog.NewJSONHandler(logFile, nil))).Handler(nil)
	httpCheck := func(r *http.Request, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("diagnostic response status %d, want %d", w.Code, want)
		}
		record("HTTP headers", w.Header())
		record("HTTP body", w.Body.String())
	}
	providerID := "diagnostic-provider-id"
	for i, status := range []int{200, 400, 409, 429, 503, -1} {
		req := Request{Key: fmt.Sprintf("diagnostic-%d", i), Recipient: recipient, Template: "provider-smoke-test/v1", Fields: map[string]string{"testID": message + " " + reset, "timestamp": "2026-11-06T12:00:00Z"}}
		id, err := q.Enqueue(ctx, nil, req)
		if err != nil {
			t.Fatal("canary enqueue failed")
		}
		record("outbox id", id)
		s.worker.sender = resend{endpoint: "https://resend.invalid/emails", key: credential, client: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			defer func() { _ = r.Body.Close() }()
			if status == -1 {
				return nil, errors.New(strings.Join(canaries, " "))
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"id":%q,"message":%q}`, providerID, strings.Join(canaries, " "))))}, nil
		})}}
		s.worker.onlyID = id
		worked, err := s.worker.Step(ctx)
		if !worked || err != nil {
			t.Fatal("provider path did not finish")
		}
		record("send error", err)
		var row outbox
		if err := q.Store.DB.First(&row, id).Error; err != nil {
			t.Fatal(err)
		}
		record("send state", row.State)
		record("send reason", row.Reason)
		req.Fields["invalid"] = reset
		_, err = q.Enqueue(ctx, nil, req)
		if err == nil {
			t.Fatal("invalid enqueue succeeded")
		}
		record("enqueue rejection", err)
	}
	webhook := func(id string, valid bool, want int) {
		t.Helper()
		body := fmt.Sprintf(`{"type":"email.delivered","data":{"email_id":%q,"to":[%q],"diagnostic":%q}}`, providerID, recipient, strings.Join(canaries, " "))
		stamp := strconv.FormatInt(now.Unix(), 10)
		mac := hmac.New(sha256.New, []byte(signing))
		_, _ = io.WriteString(mac, id+"."+stamp+"."+body)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/email", strings.NewReader(body))
		r.Header.Set("svix-id", id)
		r.Header.Set("svix-timestamp", stamp)
		r.Header.Set("svix-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		if !valid {
			r.Header.Set("svix-signature", signing)
		}
		httpCheck(r, want)
	}
	webhook("valid", true, 204)
	webhook("valid", true, 204)
	webhook("invalid", false, 400)
	if _, err := q.Store.SQL.Exec("DROP TABLE email_events"); err != nil {
		t.Fatal(err)
	}
	webhook("storage-error", true, 503)
	if _, err := q.Store.SQL.Exec("DROP TABLE email_outbox"); err != nil {
		t.Fatal(err)
	}
	httpCheck(httptest.NewRequest(http.MethodGet, "/readyz", nil), 503)
	if err := scanDiagnostics(root, canaries); err != nil {
		t.Fatal(err)
	}
}
