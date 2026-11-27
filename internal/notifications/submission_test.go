package notifications

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPushSubmissionFenceReleasesAfterRequestWriteBeforeReply(t *testing.T) {
	now := time.Now()
	started, released := 0, 0
	ctx := WithSubmissionGate(t.Context(), func() (func(), bool) { started++; return func() { released++ }, true })
	sender := &PushSender{identityLane: "app", environment: "production", topic: "dev.mychef.app", now: func() time.Time { return now }, credential: func(context.Context, string) (string, error) { return "fixture", nil }, client: &http.Client{Transport: pushRoundTrip(func(r *http.Request) (*http.Response, error) {
		if started != 1 || released != 0 {
			t.Fatal("submission fence missing before request write")
		}
		trace := httptrace.ContextClientTrace(r.Context())
		if trace == nil || trace.WroteRequest == nil {
			t.Fatal("request-write fence missing")
		}
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		if released != 1 {
			t.Fatal("provider reply still holds submission fence")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}}
	d := Delivery{IdentityLane: "app", Environment: "production", Provider: "apns", Token: strings.Repeat("a", 64), Reference: strings.Repeat("b", 32), Deadline: now.Add(time.Minute)}
	if sender.Send(ctx, d).State != Accepted || released != 1 {
		t.Fatal("submission gate was not released exactly once")
	}
}

func TestPushSubmissionFenceWithRealHTTP2WriteAndDelayedReply(t *testing.T) {
	reply, released := make(chan struct{}), make(chan struct{})
	var replyOnce sync.Once
	finish := func() { replyOnce.Do(func() { close(reply) }) }
	defer finish()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, "fixture", 500)
			return
		}
		<-reply
		w.WriteHeader(200)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	// Release the handler before Close even if an assertion fails.
	defer finish()
	now := time.Now()
	sender := &PushSender{identityLane: "app", environment: "production", topic: "dev.mychef.app", appleHost: server.URL, client: server.Client(), now: func() time.Time { return now }, credential: func(context.Context, string) (string, error) { return "fixture", nil }}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ctx = WithSubmissionGate(ctx, func() (func(), bool) { return func() { close(released) }, true })
	done := make(chan Result, 1)
	go func() {
		done <- sender.Send(ctx, Delivery{IdentityLane: "app", Environment: "production", Provider: "apns", Token: strings.Repeat("a", 64), Reference: strings.Repeat("b", 32), Deadline: now.Add(time.Minute)})
	}()
	select {
	case <-released:
	case <-ctx.Done():
		t.Fatal("real request did not release submission fence before reply")
	}
	finish()
	if result := <-done; result.State != Accepted {
		t.Fatal("real HTTP/2 fixture not accepted")
	}
}
