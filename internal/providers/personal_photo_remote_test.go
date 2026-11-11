package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func personalPhotoRemote(t *testing.T, server *httptest.Server) *Remote {
	t.Helper()
	p, err := NewRemote(Credentials{APIKey: "fixture", RecipeModel: "recipes", ImageModel: "images", CloudName: "original", CloudKey: "fixture-key", CloudSecret: "fixture-secret"}, RemoteOptions{CloudinaryEndpoint: server.URL + "/upload", CloudinaryDownloadEndpoint: server.URL + "/download", CloudinaryDeleteEndpoint: server.URL + "/destroy", TraceDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPersonalPhotoRemoteUsesReservedAuthenticatedObject(t *testing.T) {
	photo, err := NormalizePersonalPhoto(photoFixture(t, 40, 20, "png"))
	if err != nil {
		t.Fatal(err)
	}
	target := "mychef/" + strings.Repeat("a", 32)
	var calls atomic.Int32
	var started atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || !started.Load() {
			t.Error("upload dispatched before durable reservation")
		}
		key, secret, ok := r.BasicAuth()
		if !ok || key != "fixture-key" || secret != "fixture-secret" {
			t.Error("server authentication missing")
		}
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			t.Error(err)
			return
		}
		if r.FormValue("public_id") != target || r.FormValue("type") != "authenticated" || r.FormValue("overwrite") != "false" || len(r.MultipartForm.Value) != 3 {
			t.Error("upload type or exact target changed")
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = file.Close() }()
		body, _ := io.ReadAll(file)
		if header.Filename != "recipe.jpg" || !bytes.Equal(body, photo.JPEG()) || len(r.MultipartForm.File) != 1 {
			t.Error("raw bytes or source filename dispatched")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"public_id": target, "type": "authenticated", "resource_type": "image", "format": "jpg", "secure_url": "https://private.invalid/never-return"})
	}))
	defer server.Close()
	p := personalPhotoRemote(t, server)
	ctx := WithUploadIntent(t.Context(), target, func(context.Context) error { started.Store(true); return nil })
	object, err := p.UploadPersonalPhoto(ctx, photo)
	if err != nil || object.Target != target || object.Binding != "cloudinary/original" || object.DeliveryType != "authenticated" || object.Digest != photo.Digest() {
		t.Fatal("private upload identity lost", err)
	}
	encoded, _ := json.Marshal(object)
	if strings.Contains(string(encoded), "never-return") || strings.Contains(fmt.Sprint(object), target) {
		t.Fatal("private identity exposed in diagnostics")
	}
	for _, ctx := range []context.Context{t.Context(), WithUploadIntent(t.Context(), target, nil), WithUploadIntent(t.Context(), "mychef/../unsafe", func(context.Context) error { return nil }), WithUploadIntent(t.Context(), target, func(context.Context) error { return fmt.Errorf("deleted private-canary") })} {
		if _, err := p.UploadPersonalPhoto(ctx, photo); err == nil || strings.Contains(err.Error(), "private-canary") {
			t.Fatal("missing/blocked reservation accepted")
		}
	}
	if _, err := p.UploadPersonalPhoto(ctx, NormalizedPhoto{}); err == nil || calls.Load() != 1 {
		t.Fatal("invalid uploads reached provider")
	}
	entries, err := os.ReadDir(p.options.TraceDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("private photo entered generation traces", err)
	}
}

func TestPersonalPhotoRemoteDownloadStaysInternalAndChecksBytes(t *testing.T) {
	photo, err := NormalizePersonalPhoto(photoFixture(t, 40, 20, "png"))
	if err != nil {
		t.Fatal(err)
	}
	target := "mychef/" + strings.Repeat("b", 32)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		timestamp, _ := strconv.ParseInt(q.Get("timestamp"), 10, 64)
		expires, _ := strconv.ParseInt(q.Get("expires_at"), 10, 64)
		if r.Method != "GET" || r.URL.Path != "/download" || q.Get("public_id") != target || q.Get("type") != "authenticated" || q.Get("format") != "jpg" || q.Get("api_key") != "fixture-key" || len(q) != 7 || timestamp < time.Now().Unix()-5 || expires-timestamp != 60 {
			t.Error("download broadened identity or validity")
		}
		canonical := "expires_at=" + q.Get("expires_at") + "&format=jpg&public_id=" + target + "&timestamp=" + q.Get("timestamp") + "&type=authenticatedfixture-secret"
		signature := sha256.Sum256([]byte(canonical))
		if q.Get("signature") != hex.EncodeToString(signature[:]) || r.Header.Get("Authorization") != "" {
			t.Error("incorrect internal signature")
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(photo.JPEG())
	}))
	defer server.Close()
	p := personalPhotoRemote(t, server)
	data, err := p.ReadPersonalPhoto(t.Context(), target, photo.Digest())
	if err != nil || !bytes.Equal(data, photo.JPEG()) {
		t.Fatal("private photo read failed", err)
	}
	if _, err := p.ReadPersonalPhoto(t.Context(), target, strings.Repeat("0", 64)); err == nil {
		t.Fatal("unexpected provider bytes served")
	}
	for _, value := range []string{"", "https://external.invalid/a", "mychef/../x", "mychef/" + strings.Repeat("B", 32), target + "?secret"} {
		if _, err := p.ReadPersonalPhoto(t.Context(), value, photo.Digest()); err == nil {
			t.Fatal("invalid private target accepted")
		}
	}
	if _, err := p.ReadPersonalPhoto(t.Context(), target, "bad"); err == nil || calls.Load() != 2 {
		t.Fatal("invalid target or digest dispatched")
	}
}

func TestPersonalPhotoRemoteDeletionPinsDeliveryType(t *testing.T) {
	target := "mychef/" + strings.Repeat("c", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		key, secret, ok := r.BasicAuth()
		if r.Method != "POST" || r.URL.Path != "/destroy" || !ok || key != "fixture-key" || secret != "fixture-secret" || r.Form.Get("public_id") != target || r.Form.Get("type") != "authenticated" || r.Form.Get("invalidate") != "true" || len(r.Form) != 3 {
			t.Error("private deletion broadened object identity")
		}
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer server.Close()
	p := personalPhotoRemote(t, server)
	result, err := p.DeletePersonalPhoto(t.Context(), target)
	if err != nil || !result.Deleted {
		t.Fatal(err)
	}
	result, err = p.DeletePersonalPhoto(t.Context(), "mychef/../bad")
	if err == nil || !result.Permanent {
		t.Fatal("invalid delete target accepted")
	}
}

func TestPersonalPhotoRemoteRejectsMalformedAndLeakingResponses(t *testing.T) {
	photo, err := NormalizePersonalPhoto(photoFixture(t, 20, 20, "jpeg"))
	if err != nil {
		t.Fatal(err)
	}
	target := "mychef/" + strings.Repeat("d", 32)
	for _, tc := range []struct {
		status int
		body   string
	}{
		{200, `{"public_id":"foreign","type":"authenticated","resource_type":"image","format":"jpg"}`},
		{200, `{"public_id":"` + target + `","type":"upload","resource_type":"image","format":"jpg"}`},
		{200, `{"public_id":"` + target + `","type":"authenticated","resource_type":"video","format":"jpg"}`},
		{200, `{"public_id":"` + target + `","type":"authenticated","resource_type":"image","format":"png"}`},
		{200, "fixture-secret private-canary"}, {200, strings.Repeat("x", (64<<10)+1)},
		{302, "private-canary"}, {401, "fixture-secret private-canary"}, {429, "private-canary"}, {503, "private-canary"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://external.invalid/private-canary")
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		p := personalPhotoRemote(t, server)
		ctx := WithUploadIntent(t.Context(), target, func(context.Context) error { return nil })
		_, uploadErr := p.UploadPersonalPhoto(ctx, photo)
		_, readErr := p.ReadPersonalPhoto(ctx, target, photo.Digest())
		server.Close()
		for _, err := range []error{uploadErr, readErr} {
			if err == nil || strings.Contains(err.Error(), "private-canary") || strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), "signature=") {
				t.Fatal("unsafe private provider response", err)
			}
		}
	}
	// A transport failure must not return a *url.Error containing the signed URL.
	p, err := NewRemote(Credentials{APIKey: "fixture", RecipeModel: "recipes", ImageModel: "images", CloudName: "original", CloudKey: "fixture-key", CloudSecret: "fixture-secret"}, RemoteOptions{Client: &http.Client{Transport: personalPhotoFailTransport{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.ReadPersonalPhoto(t.Context(), target, photo.Digest())
	if _, ok := err.(*url.Error); ok || err == nil || strings.Contains(err.Error(), "signature") {
		t.Fatal("signed URL escaped transport failure")
	}
}

type personalPhotoFailTransport struct{}

func (personalPhotoFailTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("private transport %s", r.URL.String())
}
