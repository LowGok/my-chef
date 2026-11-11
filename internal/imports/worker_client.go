package imports

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

var ErrWorkerOutcomeUnavailable = errors.New("this import outcome is unavailable; start a new attempt if needed")

type WorkerClient struct {
	client        *http.Client
	authorization string
}

func (*WorkerClient) String() string   { return "private recipe-import client" }
func (*WorkerClient) GoString() string { return "private recipe-import client" }
func NewWorkerClient(socket string, key []byte) (*WorkerClient, error) {
	if !filepath.IsAbs(socket) || filepath.Base(socket) != "import-worker.sock" {
		return nil, ErrExtractionUnavailable
	}
	if _, err := NewDraftVault(key); err != nil {
		return nil, ErrExtractionUnavailable
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
	}, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 4096, ResponseHeaderTimeout: 55 * time.Second}
	return &WorkerClient{client: &http.Client{Transport: transport, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, authorization: WorkerAuthorization(key)}, nil
}
func (c *WorkerClient) call(ctx context.Context, method, path string, body any, result any) error {
	if c == nil || c.client == nil {
		return ErrExtractionUnavailable
	}
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil || len(encoded) > 12<<20 {
			return ErrImportInput
		}
		defer clear(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://mychef-import-worker"+path, bytes.NewReader(encoded))
	if err != nil {
		return ErrExtractionUnavailable
	}
	request.GetBody = nil
	request.Header.Set("Authorization", c.authorization)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return ErrExtractionUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == 409 {
		return ErrWorkerOutcomeUnavailable
	}
	if response.StatusCode != 200 {
		return ErrExtractionUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxPrivateDraftBytes+1))
	if err != nil || len(raw) > MaxPrivateDraftBytes {
		return ErrExtractionUnavailable
	}
	defer clear(raw)
	if _, err := boundedImportJSON(raw, MaxPrivateDraftBytes, 512<<10); err != nil {
		return ErrExtractionUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(result) != nil {
		return ErrExtractionUnavailable
	}
	return nil
}
func (c *WorkerClient) Ready(ctx context.Context) (WorkerReady, error) {
	var ready WorkerReady
	err := c.call(ctx, http.MethodGet, "/readyz", nil, &ready)
	return ready, err
}
func (c *WorkerClient) result(ctx context.Context, path string, request WorkerRequest) (WorkerResult, error) {
	var result WorkerResult
	if err := c.call(ctx, http.MethodPost, path, request, &result); err != nil {
		return result, err
	}
	if result.Private.Source.Kind != request.Source.Kind || !result.Private.Source.ImportedAt.Equal(request.Source.ImportedAt) {
		return WorkerResult{}, ErrExtractionUnavailable
	}
	if _, err := result.Private.Encode(); err != nil {
		return WorkerResult{}, ErrExtractionUnavailable
	}
	if result.Model != "" && result.Model != extractionModel {
		return WorkerResult{}, ErrExtractionUnavailable
	}
	return result, nil
}
func (c *WorkerClient) CheckPage(ctx context.Context, request WorkerRequest) (WorkerResult, error) {
	return c.result(ctx, "/v1/check-page", request)
}
func (c *WorkerClient) Extract(ctx context.Context, request WorkerRequest) (WorkerResult, error) {
	return c.result(ctx, "/v1/extract", request)
}
