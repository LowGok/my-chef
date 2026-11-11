package email

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

const resendEndpoint = "https://api.resend.com/emails"

type resend struct {
	endpoint, key string
	client        *http.Client
}

func (p resend) bindingID() string { return p.endpoint + "\x00" + p.key }

func (p resend) send(ctx context.Context, key string, body []byte) deliveryResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return deliveryResult{}
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	resp, err := p.client.Do(req)
	if err != nil {
		return deliveryResult{temporary: true}
	}
	defer func() { _ = resp.Body.Close() }()
	var reply struct {
		ID   string
		Name string
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&reply)
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if err != nil || reply.ID == "" || len(reply.ID) > 128 {
			return deliveryResult{temporary: true}
		}
		return deliveryResult{id: reply.ID}
	case resp.StatusCode == 429 || resp.StatusCode >= 500 || resp.StatusCode == 408 || (resp.StatusCode == 409 && reply.Name == "concurrent_idempotent_requests"):
		return deliveryResult{temporary: true}
	default:
		return deliveryResult{}
	}
}
