package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestHealth(t *testing.T) {
	r := httptest.NewRecorder()
	Health(r, httptest.NewRequest("GET", "/healthz", nil))
	if r.Code != 200 || r.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("health: %d %s", r.Code, r.Body.String())
	}
}
