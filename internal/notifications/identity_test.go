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

func TestPushIdentityRestrictsPackageAndRejectsRouteMismatchBeforeWire(t *testing.T) {
	for _, lane := range []string{"preview", "app"} {
		packageID := "com.gkldevelopment.mychef.preview"
		if lane == "app" {
			packageID = "dev.mychef.app"
		}
		wire := 0
		now := time.Now()
		sender := &PushSender{identityLane: lane, environment: "production", androidPackage: packageID, topic: packageID, project: "mychef-fixture", now: func() time.Time { return now }, credential: func(context.Context, string) (string, error) { return "fixture", nil }, client: &http.Client{Transport: pushRoundTrip(func(r *http.Request) (*http.Response, error) {
			wire++
			if strings.Contains(r.URL.Path, "/messages:send") {
				var body struct {
					Message struct {
						Android struct {
							Package string `json:"restricted_package_name"`
						}
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Message.Android.Package != packageID {
					t.Fatal("FCM package not restricted", err)
				}
			} else if r.Header.Get("apns-topic") != packageID {
				t.Fatal("wrong APNs topic")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"name":"projects/mychef-fixture/messages/test"}`))}, nil
		})}}
		for _, provider := range []string{"apns", "fcm"} {
			d := Delivery{IdentityLane: lane, Environment: "production", Provider: provider, Token: strings.Repeat("a", 64), Reference: strings.Repeat("b", 32), Deadline: now.Add(time.Minute)}
			if sender.Send(t.Context(), d).State != Accepted {
				t.Fatal("matching delivery denied")
			}
			before := wire
			d.IdentityLane = "foreign"
			if sender.Send(t.Context(), d).State != Permanent {
				t.Fatal("wrong lane not rejected")
			}
			d.IdentityLane = lane
			d.Environment = "development"
			if sender.Send(t.Context(), d).State != Permanent || wire != before {
				t.Fatal("mismatched delivery reached provider")
			}
		}
	}
}

func TestPushIdentityConfigurationRequiresItsExactApp(t *testing.T) {
	for _, lane := range []string{"preview", "app", "unknown"} {
		if _, err := NewIdentityPushSender(lane, &ApplePush{Topic: "foreign.app"}, nil); err == nil {
			t.Fatal("wrong APNs topic accepted")
		}
		if _, err := NewIdentityPushSender(lane, nil, &GooglePush{Package: "foreign.app"}); err == nil {
			t.Fatal("wrong FCM package accepted")
		}
	}
	if _, err := NewIdentityPushSender("app", nil, &GooglePush{}); err == nil {
		t.Fatal("candidate implicitly reused Preview's package")
	}
}
