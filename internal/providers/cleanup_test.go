package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountImageCleanupUsesOneExactAuthenticatedTarget(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		key, secret, ok := r.BasicAuth()
		if !ok || key != "key" || secret != "secret" {
			t.Error("missing server authentication")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Method != "POST" || r.Form.Get("public_id") != "mychef/fixture" || r.Form.Get("invalidate") != "true" || len(r.Form) != 2 {
			t.Error("cleanup broadened its exact target")
		}
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer server.Close()
	remote, err := NewRemote(Credentials{APIKey: "fixture", RecipeModel: "recipes", ImageModel: "images", CloudName: "original", CloudKey: "key", CloudSecret: "secret"}, RemoteOptions{CloudinaryDeleteEndpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if remote.AssetBinding() != "cloudinary/original" {
		t.Fatal("wrong provider identity")
	}
	result, err := remote.DeleteImage(t.Context(), "mychef/fixture")
	if err != nil || !result.Deleted || calls != 1 {
		t.Fatal(err)
	}
	for _, target := range []string{"", "/", "../other", "bad\nvalue", strings.Repeat("x", 256)} {
		if result, err := remote.DeleteImage(context.Background(), target); err == nil || !result.Permanent {
			t.Fatal("unsafe target accepted")
		}
	}
	if calls != 1 {
		t.Fatal("invalid target reached provider")
	}
}
func TestAccountImageCleanupRejectsProviderBodiesAndRedirects(t *testing.T) {
	cases := []struct {
		status    int
		body      string
		permanent bool
		missing   bool
	}{{401, `{"error":"private-provider-canary"}`, true, false}, {429, `{"error":"private-provider-canary"}`, false, false}, {503, `{"error":"private-provider-canary"}`, false, false}, {200, `{"result":"not found"}`, false, true}, {200, `not-json private-provider-canary`, false, false}, {302, `private-provider-canary`, false, false}}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://external.invalid/")
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		remote, err := NewRemote(Credentials{APIKey: "fixture", RecipeModel: "recipes", ImageModel: "images", CloudName: "original", CloudKey: "key", CloudSecret: "secret"}, RemoteOptions{CloudinaryDeleteEndpoint: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		result, err := remote.DeleteImage(t.Context(), "mychef/fixture")
		server.Close()
		if result.Permanent != tc.permanent || result.Missing != tc.missing || (err != nil && strings.Contains(err.Error(), "private-provider-canary")) {
			t.Fatal("unsafe provider failure classification")
		}
	}
}
