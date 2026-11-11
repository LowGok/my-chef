package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFixtureInboxRoutesAreOptIn(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/fixture/inbox"},
		{http.MethodGet, "/fixture/inbox"},
		{http.MethodPost, "/fixture/inbox"},
	} {
		req := httptest.NewRequest(route.method, route.path, nil)
		w := httptest.NewRecorder()
		New(nil, nil, Config{}, nil).Handler(nil).ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("fixture route is enabled by default: %s %s", route.method, route.path)
		}
	}
}
