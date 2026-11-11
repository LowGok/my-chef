package email

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LowGok/my-chef/internal/store"
)

func TestSmokeTemplate(t *testing.T) {
	fields := map[string]string{"testID": "<été>", "timestamp": "2026-11-06T12:00:00Z"}
	msg, err := renderTemplate("provider-smoke-test/v1", fields)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Subject != "MyChef email test" || !strings.Contains(msg.Text, "<été>") ||
		!strings.Contains(msg.HTML, "&lt;été&gt;") || strings.Contains(msg.HTML, "<été>") {
		t.Fatalf("unexpected rendering: %+v", msg)
	}
	fields["url"] = "https://example.test"
	if _, err := renderTemplate("provider-smoke-test/v1", fields); err == nil {
		t.Fatal("unexpected fields must be rejected")
	}
	delete(fields, "url")
	delete(fields, "timestamp")
	if _, err := renderTemplate("provider-smoke-test/v1", fields); err == nil {
		t.Fatal("missing timestamp must be rejected")
	}
}

type noticeCapture struct{ body []byte }

func (*noticeCapture) bindingID() string { return "notice-test" }
func (s *noticeCapture) send(_ context.Context, _ string, payload []byte) deliveryResult {
	s.body = append([]byte(nil), payload...)
	return deliveryResult{id: "notice-accepted"}
}

func TestPasswordChangedNotice(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "notice.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	queue := Queue{Store: s}
	req := Request{Key: "notice/1", Recipient: "account@example.test", Template: "password-changed/v1", Fields: map[string]string{"timestamp": "2026-11-06T12:00:00Z"}}
	if _, err := queue.Enqueue(ctx, nil, req); err != nil {
		t.Fatal(err)
	}
	sender := &noticeCapture{}
	worker := Worker{Queue: queue, sender: sender, From: "help@example.test", FingerprintKey: make([]byte, 32)}
	if worked, err := worker.Step(ctx); !worked || err != nil {
		t.Fatal("worker did not submit the queued notice")
	}
	var msg sendPayload
	if err := json.Unmarshal(sender.body, &msg); err != nil || len(msg.To) != 1 || msg.To[0] != req.Recipient || msg.Subject != "Your MyChef password changed" {
		t.Fatal("notice did not reach the fake sender")
	}
	for _, body := range []string{msg.Text, msg.HTML} {
		if !strings.Contains(body, req.Fields["timestamp"]) || !strings.Contains(body, "reply to this email") || strings.Contains(body, "https://") {
			t.Fatal("notice did not contain the time and safe reply instructions")
		}
	}
	req.Key = "notice/2"
	req.Fields["password"] = "must-not-render"
	if _, err := queue.Enqueue(ctx, nil, req); err == nil {
		t.Fatal("notice accepted an extra field")
	}
}
