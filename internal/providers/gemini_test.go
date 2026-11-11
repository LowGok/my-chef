package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LowGok/my-chef/internal/domain"
)

func TestImageGenerationUploadsWithServerSideCredentials(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	var imageCalls, uploads atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "cloud-key" || password != "cloud-secret" {
			t.Error("upload authentication missing")
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer func() { _ = file.Close() }()
		data, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(data, encoded.Bytes()) {
			t.Error("uploaded image changed")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"secure_url": "https://res.cloudinary.com/test/meal.png", "public_id": "mychef/meal"})
	}))
	defer cloud.Close()
	gemini := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imageCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"inlineData": map[string]string{"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(encoded.Bytes())}}}}}}})
	}))
	defer gemini.Close()
	p, err := NewRemote(Credentials{APIKey: "gemini-key", RecipeModel: "recipes", ImageModel: "images", CloudName: "test", CloudKey: "cloud-key", CloudSecret: "cloud-secret"}, RemoteOptions{Endpoint: gemini.URL, CloudinaryEndpoint: cloud.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Image(context.Background(), domain.RecipeContent{Title: "Dinner"})
	if err != nil || result.PublicID != "mychef/meal" || imageCalls.Load() != 1 || uploads.Load() != 1 {
		t.Fatalf("image flow: %+v %v %d %d", result, err, imageCalls.Load(), uploads.Load())
	}
}

func providerRequest() domain.GenerationRequest {
	return domain.GenerationRequest{MenuInput: domain.MenuInput{Dates: []string{"2026-12-31"}, Adults: 2, MaxMinutes: 30, Budget: "low", Region: "Belgium", Creativity: 5}, Preferences: domain.EmptyPreferences(), CurrentDate: "2026-10-27", OperationID: "1"}
}
func TestStructuredGenerationAndPrivateTrace(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("x-goog-api-key") != "test-secret-value" {
			t.Error("missing key header")
		}
		if strings.Contains(r.URL.RawQuery, "test-secret-value") {
			t.Error("key leaked into URL")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
		}
		config, ok := body["generationConfig"].(map[string]any)
		if !ok || config["responseJsonSchema"] == nil {
			t.Error("missing schema")
		}
		recipes, _ := (Fixture{}).Generate(r.Context(), providerRequest())
		content, _ := json.Marshal(map[string]any{"recipes": recipes})
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]any{"text": string(content)}}}}}})
	}))
	defer server.Close()
	root := filepath.Join(t.TempDir(), "traces")
	p, err := NewRemote(Credentials{APIKey: "test-secret-value", RecipeModel: "test-model", ImageModel: "test-image"}, RemoteOptions{Endpoint: server.URL, TraceDir: root})
	if err != nil {
		t.Fatal(err)
	}
	recipes, err := p.Generate(context.Background(), providerRequest())
	if err != nil || len(recipes) != 1 || calls.Load() != 1 {
		t.Fatalf("generate: %v %d", err, calls.Load())
	}
	if string(*recipes[0].Ingredients[0].Quantity) != "0.12345678901234567890123456789" {
		t.Fatal("quantity precision lost")
	}
	if err = filepath.Walk(root, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if info.IsDir() {
			if info.Mode().Perm() != 0o700 {
				t.Errorf("directory permissions: %v", info.Mode())
			}
			return nil
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("file permissions: %v", info.Mode())
		}
		data, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if strings.Contains(string(data), "test-secret-value") {
			t.Error("trace leaked key")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestQuotaAndRedirectAreNotRetried(t *testing.T) {
	for _, status := range []int{429, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			var redirected atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirected.Add(1)
				_, _ = io.WriteString(w, "unexpected")
			}))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "provider failure containing test-secret-value")
			}))
			defer server.Close()
			p, err := NewRemote(Credentials{APIKey: "test-secret-value", RecipeModel: "test", ImageModel: "image"}, RemoteOptions{Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = p.Generate(context.Background(), providerRequest()); err == nil {
				t.Fatal("provider failure accepted")
			}
			if calls.Load() != 1 || redirected.Load() != 0 {
				t.Fatalf("calls %d redirects %d", calls.Load(), redirected.Load())
			}
			if strings.Contains(err.Error(), "test-secret-value") {
				t.Fatal("public error leaked key")
			}
		})
	}
}
