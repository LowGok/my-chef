package email

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

type feedbackCapture struct {
	body       []byte
	sendResult deliveryResult
}

func (*feedbackCapture) bindingID() string { return "feedback-fixture" }
func (p *feedbackCapture) send(_ context.Context, _ string, body []byte) deliveryResult {
	p.body = bytes.Clone(body)
	return p.sendResult
}

func TestFeedbackDeliveryErasesPayloadAndReportsFailures(t *testing.T) {
	q, now := testOutbox(t)
	ctx := t.Context()
	u := store.User{FirstName: "Test", LastName: "Cook", Email: "contact@example.test", Username: "test", Password: "fixture"}
	if err := q.Store.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	if err := q.Store.DB.Model(&u).Update("email_verified_at", now.Unix()).Error; err != nil {
		t.Fatal(err)
	}
	actor := store.FeedbackActor{UserID: u.ID, SessionHash: "synthetic-session"}
	if err := q.Store.DB.Create(&store.Session{Hash: actor.SessionHash, UserID: &u.ID, ExpiresUnix: now.Add(time.Hour).Unix()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := q.Store.DB.Exec("INSERT INTO meals(meal_id,creator_id,creationdate,recipetitle) VALUES(17,?,'2026-11-06','Private title')", u.ID).Error; err != nil {
		t.Fatal(err)
	}
	contact := "contact@example.test"
	hostile := "<script>alert('synthetic')</script>\nBcc: unexpected@example.test"
	recipient := "operator@example.test"
	p := &feedbackCapture{}
	w := Worker{Queue: q, sender: p, From: "MyChef <help@example.test>", ReplyTo: "help@example.test", FingerprintKey: make([]byte, 32), Random: func() float64 { return .5 }}
	var refs [2]string
	for i, key := range []string{strings.Repeat("1", 32), strings.Repeat("2", 32)} {
		in := store.FeedbackInput{RequestKey: key, Topic: "recipe_issue", Message: hostile}
		if i == 0 {
			in.RecipeID = store.Ptr(int64(17))
		}
		enqueue := func(tx *gorm.DB, notice store.FeedbackNotice) (int64, error) {
			return q.Enqueue(ctx, tx, mailqueue.FeedbackRequest(recipient, notice))
		}
		got, err := q.Store.WriteFeedback(ctx, actor, in, *now, nil, enqueue)
		if err != nil {
			t.Fatal(err)
		}
		refs[i] = got.Reference
		if i == 0 {
			p.sendResult = deliveryResult{temporary: true}
		} else {
			p.sendResult = deliveryResult{id: "feedback-accepted"}
		}
		if _, err := w.Step(ctx); err != nil {
			t.Fatal(err)
		}
		var payload sendPayload
		if json.Unmarshal(p.body, &payload) != nil || len(payload.To) != 1 || payload.To[0] != recipient ||
			payload.Subject != "MyChef beta feedback" || !strings.Contains(payload.Text, "Recipe issue") || payload.ReplyTo != "help@example.test" ||
			!strings.Contains(payload.Text, contact) || !strings.Contains(payload.Text, hostile) ||
			strings.Contains(payload.HTML, "<script>") || !strings.Contains(payload.HTML, "&lt;script&gt;") ||
			strings.Contains(payload.Text, "Private title") ||
			strings.Contains(payload.Text, "legacy_meal:17") != (i == 0) {
			t.Fatal("notification did not preserve the privacy, reference or escaping contract")
		}
		if i == 0 {
			var copy string
			if err := q.Store.SQL.QueryRow("SELECT data FROM email_outbox WHERE state='queued'").Scan(&copy); err != nil || !strings.Contains(copy, contact) {
				t.Fatal("a retryable result erased the payload early")
			}
			*now = now.Add(31 * time.Second)
			p.sendResult = deliveryResult{}
			if _, err := w.Step(ctx); err != nil {
				t.Fatal(err)
			}
		}
		var data, stored string
		if err := q.Store.SQL.QueryRow(`SELECT o.data,r.message FROM feedback_reports r JOIN email_outbox o ON o.id=r.outbox_id WHERE r.reference=?`, got.Reference).Scan(&data, &stored); err != nil || data != "{}" || stored != hostile {
			t.Fatal("terminal delivery did not erase just the outbox payload")
		}
		if replay, err := q.Store.WriteFeedback(ctx, actor, in, *now, nil, enqueue); err != nil || replay != got {
			t.Fatal("terminal erasure broke feedback replay")
		}
	}
	key := []byte("synthetic-webhook-key")
	event := `{"type":"email.bounced","data":{"email_id":"feedback-accepted"}}`
	stamp := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("feedback-bounce." + stamp + "." + event))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/email", strings.NewReader(event))
	req.Header.Set("svix-id", "feedback-bounce")
	req.Header.Set("svix-timestamp", stamp)
	req.Header.Set("svix-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	res := httptest.NewRecorder()
	q.Webhook(key).ServeHTTP(res, req)
	if res.Code != 204 {
		t.Fatalf("fake bounce was not reconciled: %d", res.Code)
	}
	failures, err := q.Store.FeedbackDeliveryFailures(ctx, 0)
	if err != nil || len(failures) != 2 || failures[0].Reference != refs[1] || failures[0].Outcome != "bounced" ||
		failures[1].Reference != refs[0] || failures[1].Outcome != "provider_rejected" {
		t.Fatal("private check did not identify terminal failure and later bounce")
	}

	// Exercise both ways a temporary outage becomes terminal: the worker
	// comes back after the delivery window, or it uses all scheduled attempts.
	if err := q.Store.DB.Model(&store.Session{}).Where("hash = ?", actor.SessionHash).
		Update("expires_unix", now.Add(3*retryHorizon).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, key string
		window    bool
	}{
		{"delivery window", strings.Repeat("3", 32), true},
		{"attempt limit", strings.Repeat("4", 32), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := store.FeedbackInput{RequestKey: tc.key, Topic: "technical_issue", Message: hostile}
			receipt, err := q.Store.WriteFeedback(ctx, actor, input, *now, nil, func(tx *gorm.DB, notice store.FeedbackNotice) (int64, error) {
				return q.Enqueue(ctx, tx, mailqueue.FeedbackRequest(recipient, notice))
			})
			if err != nil {
				t.Fatal(err)
			}
			p.sendResult = deliveryResult{temporary: true}
			for attempt := 1; attempt <= maxAttempts; attempt++ {
				found, err := w.Step(ctx)
				if err != nil || !found {
					t.Fatal("worker did not process scheduled feedback")
				}
				var data, state, reason, original string
				var attempts int
				err = q.Store.SQL.QueryRow(`SELECT o.data,o.state,o.reason,o.attempts,r.message
FROM feedback_reports r JOIN email_outbox o ON o.id=r.outbox_id WHERE r.reference=?`, receipt.Reference).
					Scan(&data, &state, &reason, &attempts, &original)
				if err != nil || original != hostile {
					t.Fatal("delivery changed the accepted report")
				}
				if (tc.window && attempt == 2) || attempt == maxAttempts {
					wantAttempts := attempt
					if tc.window {
						wantAttempts = 1
					}
					if data != "{}" || state != "failed" || reason != "retry_exhausted" || attempts != wantAttempts {
						t.Fatal("retry exhaustion did not erase the notification payload")
					}
					break
				}
				if state != "queued" || !strings.Contains(data, contact) {
					t.Fatal("temporary failure did not preserve the notification for retry")
				}
				if tc.window {
					*now = now.Add(retryHorizon)
				} else {
					*now = now.Add(w.delay(attempt))
				}
			}
			if got, err := q.Store.FeedbackReceipt(ctx, actor, "feedback", input.RequestKey, *now); err != nil || got != receipt {
				t.Fatal("retry exhaustion removed the original receipt")
			}
			failures, err := q.Store.FeedbackDeliveryFailures(ctx, 0)
			if err != nil || len(failures) == 0 || failures[0].Reference != receipt.Reference || failures[0].Outcome != "retry_exhausted" {
				t.Fatal("operator lookup did not identify the exhausted notification")
			}
		})
	}

}

func TestOtherFeedbackNotification(t *testing.T) {
	q, now := testOutbox(t)
	notice := store.FeedbackNotice{
		FeedbackReceipt: store.FeedbackReceipt{Reference: strings.Repeat("a", 32), CreatedUnix: now.Unix()},
		Topic:           "other", Message: "A synthetic question about using MyChef.",
		Priority: "standard", ContactEmail: "contact@example.test",
	}
	if _, err := q.Enqueue(t.Context(), nil, mailqueue.FeedbackRequest("operator@example.test", notice)); err != nil {
		t.Fatal("Other could not be queued")
	}
	capture := &feedbackCapture{sendResult: deliveryResult{id: "other-fixture"}}
	worker := Worker{Queue: q, sender: capture, From: "MyChef <help@example.test>", ReplyTo: "help@example.test", FingerprintKey: make([]byte, 32)}
	if found, err := worker.Step(t.Context()); err != nil || !found {
		t.Fatal("Other was not sent through the fake provider")
	}
	var payload sendPayload
	if err := json.Unmarshal(capture.body, &payload); err != nil ||
		!strings.Contains(payload.Text, "Contact reason: Other") ||
		!strings.Contains(payload.HTML, "Contact reason: Other") ||
		len(payload.To) != 1 || payload.To[0] != "operator@example.test" {
		t.Fatal("Other notification did not retain the operator label and recipient")
	}
}
