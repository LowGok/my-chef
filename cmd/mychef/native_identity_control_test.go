package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

func nativeIdentityControlTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "myc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestNativeIdentityControlRoundTrip(t *testing.T) {
	dir := nativeIdentityControlTestDir(t)
	s, err := store.Open(filepath.Join(dir, "fixture.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "native-identity.sock")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	closeControl, err := startNativeIdentityControl(s, socket, true, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeControl)
	info, err := os.Lstat(socket)
	if err != nil || info.Mode().Perm() != 0600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatal("operator listener is not a private Unix socket", err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	call := func(method, body string, want int) store.NativeIdentityPolicy {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, "http://localhost/policy", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != want {
			t.Fatalf("unexpected operator response: got %d want %d", resp.StatusCode, want)
		}
		var policy store.NativeIdentityPolicy
		if want == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&policy); err != nil {
				t.Fatal(err)
			}
		}
		return policy
	}
	if initial := call("GET", "", 200); initial.Active || initial.Revision != 0 {
		t.Fatal("operator control reopened paused policy")
	}
	for _, invalid := range []string{
		`{"active":true}`,
		`{"expectedRevision":0}`,
		`{"expectedRevision":0,"active":true,"identity":"preview"}`,
		`{"expectedRevision":0,"active":true} {}`,
	} {
		call("PUT", invalid, 400)
	}
	if current := call("PUT", `{"expectedRevision":0,"active":true}`, 200); !current.Active || current.Revision != 1 {
		t.Fatal("operator control did not activate the exact candidate revision")
	}
	s.NativeIdentityWork.RLock()
	result := make(chan error, 1)
	go func() {
		request, err := http.NewRequestWithContext(t.Context(), "PUT", "http://localhost/policy", strings.NewReader(`{"expectedRevision":1,"active":false}`))
		if err == nil {
			request.Header.Set("Content-Type", "application/json")
			var response *http.Response
			response, err = client.Do(request)
			if err == nil {
				_ = response.Body.Close()
				if response.StatusCode != 200 {
					err = io.ErrUnexpectedEOF
				}
			}
		}
		result <- err
	}()
	select {
	case err := <-result:
		s.NativeIdentityWork.RUnlock()
		t.Fatalf("withdrawal finished while admitted work held the fence: %v", err)
	case <-time.After(50 * time.Millisecond):
		s.NativeIdentityWork.RUnlock()
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	call("PUT", `{"expectedRevision":1,"active":true}`, 409)
	if current := call("GET", "", 200); current.Active || current.Revision != 2 {
		t.Fatal("a stale request reopened withdrawn policy")
	}
}

func TestNativeIdentityControlRejectsInsecureTargets(t *testing.T) {
	dir := nativeIdentityControlTestDir(t)
	socket := filepath.Join(dir, "native-identity.sock")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if closeControl, err := startNativeIdentityControl(nil, "", false, logger); err != nil {
		t.Fatal(err)
	} else {
		closeControl()
	}
	if _, err := startNativeIdentityControl(nil, socket, false, logger); err == nil {
		t.Fatal("control enabled without app ingress")
	}
	if err := os.WriteFile(socket, []byte("preserve this file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := startNativeIdentityControl(nil, socket, true, logger); err == nil {
		t.Fatal("control replaced an existing file")
	}
	if content, _ := os.ReadFile(socket); string(content) != "preserve this file" {
		t.Fatal("existing socket target was changed")
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0750); err != nil {
		t.Fatal(err)
	}
	if _, err := startNativeIdentityControl(nil, filepath.Join(dir, "native-identity.sock"), true, logger); err == nil {
		t.Fatal("control accepted a directory readable by other identities")
	}
}
