package billing

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const playPullBatch = 5

var (
	pubsubProject      = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	pubsubSubscription = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{2,254}$`)
	pubsubMessage      = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
)

// PlayNotice is a lookup hint from the configured Pub/Sub subscription.
// It never asserts account ownership, payment or revocation.
type PlayNotice struct{ ID, Token string }

type PlayPullConfig struct {
	Project, Subscription, Package string
	AccessToken                    func(context.Context) (string, error)
}

type PlayPull struct {
	cfg  PlayPullConfig
	url  string
	http *http.Client
}

func NewPlayPull(cfg PlayPullConfig, client *http.Client) (*PlayPull, error) {
	if !pubsubProject.MatchString(cfg.Project) || !pubsubSubscription.MatchString(cfg.Subscription) ||
		strings.HasPrefix(cfg.Subscription, "goog") || !playPackage.MatchString(cfg.Package) || len(cfg.Package) > 150 ||
		cfg.AccessToken == nil {
		return nil, ErrStoreUnavailable
	}
	transport := http.DefaultTransport
	if client != nil && client.Transport != nil {
		transport = client.Transport
	}
	return &PlayPull{cfg: cfg,
		url: "https://pubsub.googleapis.com/v1/projects/" + cfg.Project + "/subscriptions/" + cfg.Subscription,
		http: &http.Client{Transport: transport, Timeout: 35 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

type playPullReply struct {
	Messages []struct {
		Ack     string `json:"ackId"`
		Message struct {
			ID   string `json:"messageId"`
			Data []byte `json:"data"`
		} `json:"message"`
	} `json:"receivedMessages"`
}

type playNoticePayload struct {
	Version      string `json:"version"`
	Package      string `json:"packageName"`
	EventTime    string `json:"eventTimeMillis"`
	Subscription *struct {
		Version string `json:"version"`
		Type    int    `json:"notificationType"`
		Token   string `json:"purchaseToken"`
	} `json:"subscriptionNotification"`
	Voided *struct {
		ProductType int    `json:"productType"`
		Token       string `json:"purchaseToken"`
	} `json:"voidedPurchaseNotification"`
	Other        *struct{} `json:"oneTimeProductNotification"`
	Test         *struct{} `json:"testNotification"`
	RefundReview *struct{} `json:"pendingRefundReviewNotification"`
}

func playNoticeToken(data []byte, app string) (string, bool) {
	var event playNoticePayload
	if len(data) == 0 || len(data) > 8192 || json.Unmarshal(data, &event) != nil ||
		event.Version != "1.0" || event.Package != app {
		return "", false
	}
	at, err := strconv.ParseInt(event.EventTime, 10, 64)
	if err != nil || at <= 0 {
		return "", false
	}
	count := 0
	for _, present := range []bool{event.Subscription != nil, event.Voided != nil,
		event.Other != nil, event.Test != nil, event.RefundReview != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return "", false
	}
	token := ""
	switch {
	case event.Subscription != nil:
		if event.Subscription.Version != "1.0" || event.Subscription.Type <= 0 {
			return "", false
		}
		token = event.Subscription.Token
	case event.Voided != nil:
		switch event.Voided.ProductType {
		case 1:
			token = event.Voided.Token
		case 2:
			return "", true // An unrelated one-time order has no subscription work.
		default:
			return "", false
		}
	default:
		return "", true // Test, one-time purchase and nonterminal refund review.
	}
	return token, validPlayToken(token)
}

// Poll acknowledges supported notices only after retain confirms the event has
// been verified and its work persisted. It leaves failed writes for redelivery;
// malformed and foreign messages are never lookup or acknowledgement targets.
func (p *PlayPull) Poll(parent context.Context, retain func(context.Context, PlayNotice) error) (int, error) {
	if retain == nil {
		return 0, ErrStoreUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	bearer, err := p.cfg.AccessToken(ctx)
	if err != nil || !validPlayBearer(bearer) {
		return 0, ErrStoreUnavailable
	}
	var reply playPullReply
	if err := p.post(ctx, bearer, ":pull", struct {
		Max int `json:"maxMessages"`
	}{playPullBatch}, &reply); err != nil {
		return 0, err
	}
	if len(reply.Messages) > playPullBatch {
		return 0, ErrStoreUnavailable
	}
	acks := make([]string, 0, len(reply.Messages))
	processed, bad := 0, false
	for _, entry := range reply.Messages {
		token, valid := playNoticeToken(entry.Message.Data, p.cfg.Package)
		if !valid || !pubsubMessage.MatchString(entry.Message.ID) || len(entry.Ack) == 0 || len(entry.Ack) > 4096 ||
			strings.ContainsAny(entry.Ack, "\r\n\t ") {
			bad = true
			continue
		}
		if token != "" {
			if err := retain(ctx, PlayNotice{ID: entry.Message.ID, Token: token}); err != nil {
				bad = true
				continue
			}
			processed++
		}
		acks = append(acks, entry.Ack)
	}
	if len(acks) != 0 {
		if err := p.post(ctx, bearer, ":acknowledge", struct {
			IDs []string `json:"ackIds"`
		}{acks}, nil); err != nil {
			return processed, err
		}
	}
	if bad {
		return processed, ErrStoreUnavailable
	}
	return processed, nil
}

func (p *PlayPull) post(ctx context.Context, bearer, operation string, body, response any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return ErrStoreUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url+operation, bytes.NewReader(raw))
	if err != nil {
		return ErrStoreUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	res, err := p.http.Do(req)
	if err != nil {
		return ErrStoreUnavailable
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return ErrStoreUnavailable
	}
	const limit = 128 << 10
	raw, err = io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || len(raw) > limit || response != nil && json.Unmarshal(raw, response) != nil {
		return ErrStoreUnavailable
	}
	return nil
}
