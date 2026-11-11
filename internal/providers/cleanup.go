package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/LowGok/my-chef/internal/store"
)

func (p *Remote) AssetBinding() string { return "cloudinary/" + p.credentials.CloudName }
func (Fixture) AssetBinding() string   { return "fixture" }
func (Fixture) DeleteImage(context.Context, string) (store.AssetDeletionResult, error) {
	return store.AssetDeletionResult{Missing: true}, nil
}

func validCleanupPublicID(value string) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 255 || len(value) > 1024 || strings.TrimSpace(value) != value || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return !strings.ContainsAny(value, "?&#\\%<>+") && strings.IndexFunc(value, unicode.IsControl) < 0
}
func (p *Remote) DeleteImage(ctx context.Context, target string) (store.AssetDeletionResult, error) {
	if !validCleanupPublicID(target) {
		return store.AssetDeletionResult{Permanent: true}, Failure("provider_invalid")
	}
	return p.deleteImage(ctx, target, "")
}

func (p *Remote) deleteImage(ctx context.Context, target, deliveryType string) (store.AssetDeletionResult, error) {
	if p.credentials.CloudName == "" || p.credentials.CloudKey == "" || p.credentials.CloudSecret == "" {
		return store.AssetDeletionResult{}, Failure("provider_unavailable")
	}
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	form := url.Values{"public_id": {target}, "invalidate": {"true"}}
	if deliveryType != "" {
		form.Set("type", deliveryType)
	}
	request, err := http.NewRequestWithContext(call, http.MethodPost, p.options.CloudinaryDeleteEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return store.AssetDeletionResult{}, Failure("provider_unavailable")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(p.credentials.CloudKey, p.credentials.CloudSecret)
	response, err := p.options.Client.Do(request)
	if err != nil {
		return store.AssetDeletionResult{}, Failure("provider_unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return store.AssetDeletionResult{Permanent: response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != 408 && response.StatusCode != 429}, Failure("provider_unavailable")
	}
	var result struct {
		Result string `json:"result"`
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 || json.Unmarshal(body, &result) != nil {
		return store.AssetDeletionResult{}, Failure("provider_invalid")
	}
	switch result.Result {
	case "ok":
		return store.AssetDeletionResult{Deleted: true}, nil
	case "not found", "not_found":
		return store.AssetDeletionResult{Missing: true}, nil
	default:
		return store.AssetDeletionResult{}, Failure("provider_invalid")
	}
}
