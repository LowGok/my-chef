package billing

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPlayPullAcknowledgesOnlyAfterDurableRetention(t *testing.T) {
	event := func(id, ack, pkg, token string) map[string]any {
		data := `{"version":"1.0","packageName":"` + pkg + `","eventTimeMillis":"1794690000000","subscriptionNotification":{"version":"1.0","notificationType":4,"purchaseToken":"` + token + `"}}`
		return map[string]any{"ackId": ack, "message": map[string]string{"messageId": id,
			"data": base64.StdEncoding.EncodeToString([]byte(data))}}
	}
	const pkg = "com.example.fixture"
	const route = "/v1/projects/fixture-project/subscriptions/fixture-notices"
	body, err := json.Marshal(map[string]any{"receivedMessages": []any{
		event("1", "ack-safe", pkg, "fixture-token-a"),
		event("2", "ack-retry", pkg, "fixture-token-b"),
		event("3", "ack-foreign", "com.other.fixture", "fixture-token-c"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var recorded []string
	acknowledged := false
	transport := playTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "pubsub.googleapis.com" || r.URL.Scheme != "https" ||
			r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer offline-pubsub-access" {
			t.Fatal("request left the configured Pub/Sub route")
		}
		requestBody, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case route + ":pull":
			if string(requestBody) != `{"maxMessages":5}` {
				t.Fatal("unexpected pull settings")
			}
			return playTestReply(200, string(body)), nil
		case route + ":acknowledge":
			if string(requestBody) != `{"ackIds":["ack-safe"]}` || strings.Join(recorded, " ") != "1 2" {
				t.Fatal("acked before retention or included an unretained notice")
			}
			acknowledged = true
			return playTestReply(200, "{}"), nil
		default:
			t.Fatal("operation left the configured subscription")
			return nil, ErrStoreUnavailable
		}
	})
	pull, err := NewPlayPull(PlayPullConfig{Project: "fixture-project", Subscription: "fixture-notices", Package: pkg,
		AccessToken: func(context.Context) (string, error) { return "offline-pubsub-access", nil }},
		&http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	n, err := pull.Poll(t.Context(), func(_ context.Context, notice PlayNotice) error {
		recorded = append(recorded, notice.ID)
		if notice.ID == "1" && notice.Token == "fixture-token-a" {
			return nil
		}
		if notice.ID == "2" && notice.Token == "fixture-token-b" {
			return errors.New("fixture write failure")
		}
		t.Fatal("foreign or malformed notice reached retention")
		return nil
	})
	if n != 1 || !errors.Is(err, ErrStoreUnavailable) || !acknowledged {
		t.Fatal("did not preserve the successfully retained notice or retry the failed write")
	}
}

func TestPlayPullDoesNotAckUnroutedNoticesOrFollowRedirects(t *testing.T) {
	adapter, err := NewPlayPull(PlayPullConfig{Project: "fixture-project", Subscription: "fixture-notices",
		Package: "com.example.fixture", AccessToken: func(context.Context) (string, error) { return "offline-token", nil }},
		&http.Client{Transport: playTestTransport(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, ":pull") || r.URL.Host != "pubsub.googleapis.com" {
				t.Fatal("unapproved Pub/Sub request")
			}
			result := playTestReply(http.StatusTemporaryRedirect, "")
			result.Header.Set("Location", "https://foreign.example.test")
			return result, nil
		})})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := adapter.Poll(t.Context(), func(context.Context, PlayNotice) error {
		t.Fatal("processed a redirected response")
		return nil
	}); n != 0 || !errors.Is(err, ErrStoreUnavailable) {
		t.Fatal("expected rejection")
	}
}
