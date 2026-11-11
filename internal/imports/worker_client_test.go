package imports

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerClientPrivateSocketAndUnavailableOutcome(t *testing.T) {
	ledger, _, _, _, policy, binding, now := admissionFixture(t)
	key := bytes.Repeat([]byte{33}, 32)
	engine := &testExtractionEngine{result: extractionFixture()}
	handler, err := NewWorkerHandler(WorkerOptions{Ledger: ledger, Binding: binding, Policy: func() (ExtractionPolicy, error) { return policy, nil }, Engine: engine, Fetcher: &workerPageFixture{html: []byte("<main>Written recipe.</main>")}, ChannelKey: key, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../../.test-artifacts")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(root, "iw-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "import-worker.sock")
	listener, err := ListenPrivateWorker(socket, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	defer func() { _ = server.Close(); <-done }()
	client, err := NewWorkerClient(socket, key)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := client.Ready(t.Context())
	if err != nil || !ready.Ready || !ready.AIAvailable {
		t.Fatal("private readiness failed", err)
	}
	request := WorkerRequest{OperationID: strings.Repeat("1", 32), AccountID: strings.Repeat("a", 32), Source: ImportSource{Kind: "url", URL: "https://recipes.example.com/plain", ImportedAt: now}, Text: "Ingredients: carrots. Instructions: cook gently."}
	result, err := client.Extract(t.Context(), request)
	if err != nil || result.Private.Draft == nil {
		t.Fatal("private extraction failed", err)
	}
	if _, err := client.Extract(t.Context(), request); err != ErrWorkerOutcomeUnavailable || engine.calls.Load() != 1 {
		t.Fatal("RPC retry redispatched", err)
	}
	invalid, _ := NewWorkerClient(socket, bytes.Repeat([]byte{34}, 32))
	if _, err := invalid.Ready(context.Background()); err == nil {
		t.Fatal("wrong channel key authenticated")
	}
}

type cancelledExtractionEngine struct {
	started, stopped chan struct{}
	calls            atomic.Int64
}

func (e *cancelledExtractionEngine) extract(ctx context.Context, _ ExtractionInput, _ ExtractionLimits) (ExtractedRecipe, error) {
	e.calls.Add(1)
	close(e.started)
	<-ctx.Done()
	close(e.stopped)
	return ExtractedRecipe{}, ctx.Err()
}
func TestWorkerClientCancellationCannotReplayDispatchedExtraction(t *testing.T) {
	ledger, _, _, _, policy, binding, now := admissionFixture(t)
	key := bytes.Repeat([]byte{35}, 32)
	engine := &cancelledExtractionEngine{started: make(chan struct{}), stopped: make(chan struct{})}
	handler, err := NewWorkerHandler(WorkerOptions{Ledger: ledger, Binding: binding, Policy: func() (ExtractionPolicy, error) { return policy, nil }, Engine: engine, Fetcher: &workerPageFixture{}, ChannelKey: key, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../../.test-artifacts")
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(root, "iw-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "import-worker.sock")
	listener, err := ListenPrivateWorker(socket, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	defer func() { _ = server.Close(); <-done }()
	client, err := NewWorkerClient(socket, key)
	if err != nil {
		t.Fatal(err)
	}
	request := WorkerRequest{OperationID: strings.Repeat("3", 32), AccountID: strings.Repeat("a", 32), Source: ImportSource{Kind: "url", URL: "https://recipes.example.com/plain", ImportedAt: now}, Text: "Ingredients: carrots. Instructions: cook gently."}
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	go func() { _, err := client.Extract(ctx, request); finished <- err }()
	select {
	case <-engine.started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("extraction never dispatched")
	}
	cancel()
	if err := <-finished; err == nil {
		t.Fatal("cancel returned success")
	}
	select {
	case <-engine.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("provider context did not cancel")
	}
	if _, err := client.Extract(t.Context(), request); err != ErrWorkerOutcomeUnavailable || engine.calls.Load() != 1 {
		t.Fatal("cancelled provider operation replayed", err)
	}
}
