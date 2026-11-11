package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReservedUploadUsesExactIdentityAndFencesDispatch(t *testing.T) {
	target := "mychef/" + strings.Repeat("a", 32)
	var calls atomic.Int32
	started := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !started {
			t.Error("provider dispatch preceded durable intent")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		if r.FormValue("public_id") != target || r.FormValue("overwrite") != "false" || r.FormValue("folder") != "" {
			t.Error("upload did not use its immutable exact target")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"public_id": target, "secure_url": "https://res.cloudinary.com/test/image.png"})
	}))
	defer server.Close()
	p, err := NewRemote(Credentials{APIKey: "fixture", RecipeModel: "recipes", ImageModel: "images", CloudName: "test", CloudKey: "key", CloudSecret: "secret"}, RemoteOptions{CloudinaryEndpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithUploadIntent(t.Context(), target, func(context.Context) error { started = true; return nil })
	if _, err := p.upload(ctx, []byte("fixture-image"), "image/png"); err != nil {
		t.Fatal(err)
	}
	blocked := WithUploadIntent(t.Context(), target, func(context.Context) error { return errors.New("account deleted") })
	if _, err := p.upload(blocked, []byte("fixture-image"), "image/png"); err == nil || calls.Load() != 1 {
		t.Fatal("blocked owner reached provider", err)
	}
}
func TestTraceGuardPrecedesFilesAndBlocksLateResponse(t *testing.T) {
	root := t.TempDir()
	p, err := NewRemote(Credentials{APIKey: "fixture", RecipeModel: "recipes", ImageModel: "images"}, RemoteOptions{TraceDir: root})
	if err != nil {
		t.Fatal(err)
	}
	allowed := true
	var guarded string
	ctx := WithTraceGuard(t.Context(), func(path string, write func() error) error {
		guarded = path
		if !allowed {
			return errors.New("account deleted")
		}
		return write()
	})
	path, err := p.beginTrace(ctx, "17", map[string]string{"private": "owned-request"})
	if err != nil || path != guarded {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "request.json")); err != nil {
		t.Fatal(err)
	}
	allowed = false
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	p.finishTrace(ctx, path, []byte("late-private-response"), 200, nil)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("late provider response recreated trace")
	}
}
