package httpapi

import (
	"bytes"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/LowGok/my-chef/internal/store"
)

func TestEmailClientAddressTrustsOnlyConfiguredPeerAndSingleValidAddress(t *testing.T) {
	trusted := []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	for _, tc := range []struct {
		name, peer string
		forwarded  []string
		want       string
	}{
		{"trusted first client", "127.0.0.1:1000", []string{"203.0.113.1"}, "203.0.113.1"},
		{"trusted second client", "127.0.0.1:1000", []string{"203.0.113.2"}, "203.0.113.2"},
		{"untrusted", "192.0.2.3:1000", []string{"203.0.113.1"}, "192.0.2.3"},
		{"list", "127.0.0.1:1000", []string{"203.0.113.1, 203.0.113.2"}, "127.0.0.1"},
		{"multiple", "127.0.0.1:1000", []string{"203.0.113.1", "203.0.113.2"}, "127.0.0.1"},
		{"malformed", "127.0.0.1:1000", []string{"not an address"}, "127.0.0.1"},
		{"mapped IPv4", "[::ffff:192.0.2.3]:1000", nil, "192.0.2.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/v1/register", nil)
			req.RemoteAddr = tc.peer
			req.Header["X-Forwarded-For"] = tc.forwarded
			if actual := emailClientAddress(req, trusted); actual != tc.want {
				t.Fatalf("wrong source bucket: %s", actual)
			}
		})
	}
}

func TestEmailRateBucketUsesASeparateStableKeyAndPurpose(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	input := "tester@example.test"
	first, err := store.EmailRateBucket(key, "address", input)
	if err != nil || first == "" || first == input {
		t.Fatal("no protected bucket")
	}
	again, err := store.EmailRateBucket(key, "address", input)
	if err != nil || again != first {
		t.Fatal("bucket changed across equivalent inputs")
	}
	other, err := store.EmailRateBucket(key, "source", input)
	if err != nil || other == first {
		t.Fatal("bucket purposes collided")
	}
	if _, err := store.EmailRateBucket(nil, "address", input); err == nil {
		t.Fatal("missing key allowed unprotected identity")
	}
}
