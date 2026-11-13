package notifications

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type pushRoundTrip func(*http.Request) (*http.Response, error)

func (f pushRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReminderProvidersUseOneSubmissionAndExplicitExpiry(t *testing.T) {
	now := time.Date(2026, 11, 16, 16, 0, 0, 0, time.UTC)
	for _, provider := range []string{"apns", "fcm"} {
		t.Run(provider, func(t *testing.T) {
			for _, status := range []int{0, 200, 201, 429, 503, 504} {
				if status == 201 && provider == "apns" {
					continue
				}
				wire := 0
				transport := pushRoundTrip(func(req *http.Request) (*http.Response, error) {
					wire++
					if req.GetBody != nil || req.Header.Get("Idempotency-Key") != "" {
						t.Fatal("send can be automatically replayed")
					}
					var body map[string]any
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if provider == "apns" {
						if req.Header.Get("apns-expiration") != "1794844860" || req.Header.Get("apns-topic") != "com.example.preview" || body["reminder"] != "0123456789abcdef0123456789abcdef" {
							t.Fatal("APNs payload or lifetime was wrong", req.Header.Get("apns-expiration"))
						}
					} else {
						message := body["message"].(map[string]any)
						if message["android"].(map[string]any)["ttl"] != "60s" || message["data"].(map[string]any)["reminder"] != "0123456789abcdef0123456789abcdef" {
							t.Fatal("FCM payload or lifetime was wrong")
						}
					}
					if status == 0 {
						return nil, io.ErrUnexpectedEOF
					}
					payload := "{}"
					response := status
					switch status {
					case 200:
						if provider == "fcm" {
							payload = `{"name":"projects/mychef-fake/messages/fixture"}`
						}
					case 201:
						// This subcase models a successful status with a malformed FCM body.
						response = 200
						payload = `{"name":`
					case 429:
						payload = `{"reason":"TooManyRequests","error":{"status":"RESOURCE_EXHAUSTED"}}`
					case 503:
						payload = `{"reason":"ServiceUnavailable","error":{"status":"UNAVAILABLE"}}`
					}
					return &http.Response{StatusCode: response, Header: http.Header{"Retry-After": {"120"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
				})
				sender := &PushSender{topic: "com.example.preview", project: "mychef-fake", client: &http.Client{Transport: transport}, credential: func(context.Context, string) (string, error) { return "synthetic", nil }, now: func() time.Time { return now }}
				got := sender.Send(t.Context(), Delivery{Provider: provider, Token: strings.Repeat("a", 64), Reference: "0123456789abcdef0123456789abcdef", Deadline: now.Add(time.Minute)})
				want := map[int]State{0: Uncertain, 200: Accepted, 201: Uncertain, 429: Temporary, 503: Temporary, 504: Uncertain}[status]
				delay := 2 * time.Minute
				if provider == "apns" && status == 503 {
					delay = 15 * time.Minute
				}
				if wire != 1 || got.State != want || want == Temporary && got.RetryAfter != delay {
					t.Fatalf("status %d: %d submissions, result %+v", status, wire, got)
				}
			}
		})
	}
}
