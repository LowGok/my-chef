package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

var errNativeIdentityControl = errors.New("invalid private native identity control configuration")

// The operator socket belongs to the same process as request and push
// admission. Filesystem permissions keep its handler off the app's TCP server.
func startNativeIdentityControl(s *store.Store, path string, ingress bool, logger *slog.Logger) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	if !ingress || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != "native-identity.sock" {
		return nil, errNativeIdentityControl
	}
	directory := filepath.Dir(path)
	uid := uint32(os.Geteuid())
	for dir := directory; ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() {
			return nil, errNativeIdentityControl
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != uid && stat.Uid != 0) ||
			(dir == directory && (stat.Uid != uid || info.Mode().Perm() != 0700)) ||
			(info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return nil, errNativeIdentityControl
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		// An occupied path may still belong to another process. Preserve it.
		return nil, errNativeIdentityControl
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return nil, errNativeIdentityControl
	}
	server := &http.Server{
		Handler:           nativeIdentityControl(s),
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      25 * time.Second,
		IdleTimeout:       5 * time.Second,
		MaxHeaderBytes:    4096,
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("native identity operator socket stopped")
		}
	}()
	return func() { _ = server.Close() }, nil
}

func nativeIdentityControl(s *store.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /policy", func(w http.ResponseWriter, r *http.Request) {
		s.NativeIdentityWork.RLock()
		policy, err := s.NativeIdentityPolicy(r.Context())
		s.NativeIdentityWork.RUnlock()
		if err != nil {
			http.Error(w, "Native identity policy unavailable", http.StatusServiceUnavailable)
			return
		}
		writeNativeIdentityPolicy(w, policy)
	})
	mux.HandleFunc("PUT /policy", func(w http.ResponseWriter, r *http.Request) {
		var change struct {
			ExpectedRevision *int64 `json:"expectedRevision"`
			Active           *bool  `json:"active"`
			SyncUntil        int64  `json:"syncUntil"`
		}
		reader := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512))
		reader.DisallowUnknownFields()
		if reader.Decode(&change) != nil || change.ExpectedRevision == nil || change.Active == nil ||
			reader.Decode(new(any)) != io.EOF {
			http.Error(w, "Invalid native identity policy request", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		policy, err := s.SetNativeIdentityPolicy(ctx, *change.ExpectedRevision, *change.Active, change.SyncUntil, time.Now())
		if err != nil {
			http.Error(w, "Policy change unconfirmed. Read the current revision.", http.StatusConflict)
			return
		}
		writeNativeIdentityPolicy(w, policy)
	})
	return mux
}

func writeNativeIdentityPolicy(w http.ResponseWriter, policy store.NativeIdentityPolicy) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(policy)
}
