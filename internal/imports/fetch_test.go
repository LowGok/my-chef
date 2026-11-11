package imports

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

type peerConn struct {
	net.Conn
	peer net.Addr
}

func (c peerConn) RemoteAddr() net.Addr { return c.peer }

func fetchFixture(t *testing.T, handler http.Handler) (*Fetcher, *atomic.Int64) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	if err := server.Certificate().VerifyHostname("example.com"); err != nil {
		t.Fatal("TLS fixture does not cover the requested public name")
	}
	f := NewFetcher()
	f.resolver = resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	})
	f.roots = x509.NewCertPool()
	f.roots.AddCert(server.Certificate())
	var connections atomic.Int64
	f.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "93.184.216.34:443" {
			t.Error("transport did not dial the validated literal address")
			return nil, errors.New("unexpected test dial")
		}
		connections.Add(1)
		conn, err := (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		if err != nil {
			return nil, err
		}
		return peerConn{Conn: conn, peer: &net.TCPAddr{IP: net.ParseIP("93.184.216.34"), Port: 443}}, nil
	}
	return f, &connections
}

func TestRecipeFetchUsesPinnedPublicTLSAndNoAmbientCredentials(t *testing.T) {
	var pages atomic.Int64
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	f, connections := fetchFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		pages.Add(1)
		if r.Host != "example.com" || r.Header.Get("User-Agent") != recipeImportAgent || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" {
			t.Error("request headers escaped the dedicated fetch boundary")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html>One written recipe.</html>"))
	}))
	page, err := f.Fetch(t.Context(), "https://example.com/recipe#steps")
	if err != nil || string(page.HTML) != "<html>One written recipe.</html>" || page.URL != "https://example.com/recipe" || pages.Load() != 1 || connections.Load() != 2 {
		t.Fatal("bounded public recipe fetch failed", err)
	}
}

func TestRecipeFetchRejectsForbiddenTargetsWithoutConnecting(t *testing.T) {
	for _, raw := range []string{
		"http://example.com/recipe", "file:///etc/passwd", "https://user:secret@example.com/recipe", "https://example.com:8443/recipe",
		"https://localhost/recipe", "https://site.local/recipe", "https://site.internal/recipe", "https://site.test/recipe",
		"https://127.0.0.1/", "https://169.254.169.254/", "https://2130706433/", "https://0177.0.0.1/", "https://127.1/", "https://0x7f000001/",
		"https://[::1]/", "https://[fe80::1%25eth0]/", "https://[::ffff:169.254.169.254]/",
		"https://example.com/ok/../blocked", "https://example.com/ok/%2e%2e/blocked", "https://example.com/%252e%252e/blocked",
	} {
		t.Run(raw, func(t *testing.T) {
			f := NewFetcher()
			var calls int
			f.resolver = resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
			})
			f.dial = func(context.Context, string, string) (net.Conn, error) {
				calls++
				return nil, errors.New("forbidden test connection")
			}
			if _, err := f.Fetch(t.Context(), raw); !errors.Is(err, ErrRecipeSourceUnavailable) || calls != 0 {
				t.Fatal("forbidden URL connected or returned an unsafe error")
			}
		})
	}
}

func TestRecipeFetchRejectsEveryReservedDNSAnswer(t *testing.T) {
	for _, address := range []string{
		"0.0.0.0", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.169.254", "172.16.0.1", "192.0.0.1", "192.0.2.1", "192.168.1.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1",
		"::", "::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "100::1", "2001::1", "2001:db8::1", "2002:7f00:1::", "3fff::1", "fc00::1", "fe80::1", "ff02::1",
	} {
		t.Run(address, func(t *testing.T) {
			f := NewFetcher()
			var calls int
			f.resolver = resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr(address)}, nil
			})
			f.dial = func(context.Context, string, string) (net.Conn, error) {
				calls++
				return nil, errors.New("forbidden test connection")
			}
			if _, err := f.Fetch(t.Context(), "https://example.com/recipe"); !errors.Is(err, ErrRecipeSourceUnavailable) || calls != 0 {
				t.Fatal("mixed public/private DNS answers allowed a connection")
			}
		})
	}
}

func TestRecipeFetchRechecksDNSAndConnectedPeer(t *testing.T) {
	var pages atomic.Int64
	f, connections := fetchFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(404)
			return
		}
		pages.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>recipe</html>"))
	}))
	var lookups int
	f.resolver = resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		if lookups == 1 {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	if _, err := f.Fetch(t.Context(), "https://example.com/recipe"); !errors.Is(err, ErrRecipeSourceUnavailable) || pages.Load() != 0 || connections.Load() != 1 {
		t.Fatal("DNS rebinding reached a recipe connection")
	}
	f.resolver = resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	})
	original := f.dial
	f.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := original(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return peerConn{Conn: conn, peer: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 443}}, nil
	}
	if _, err := f.Fetch(t.Context(), "https://example.com/recipe"); !errors.Is(err, ErrRecipeSourceUnavailable) || pages.Load() != 0 {
		t.Fatal("connected peer mismatch escaped validation")
	}
}

func TestRecipeFetchBoundsRedirectsAndDoesNotCarryCookies(t *testing.T) {
	var pages atomic.Int64
	f, _ := fetchFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(404)
			return
		}
		pages.Add(1)
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("redirect carried credentials")
		}
		w.Header().Set("Set-Cookie", "private=not-forwarded")
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	if _, err := f.Fetch(t.Context(), "https://example.com/start"); !errors.Is(err, ErrRecipeSourceUnavailable) || pages.Load() != 4 {
		t.Fatal("redirect loop exceeded three redirects")
	}
}

func TestRecipeFetchHonorsRobotsAndAccessRefusals(t *testing.T) {
	for _, robots := range []string{
		"User-agent: *\nDisallow: /private\n",
		"User-agent: MyChef\nDisallow: /\nUser-agent: *\nAllow: /\n",
		"User-agent: *\nDisallow: /*secret$\n",
	} {
		var pages atomic.Int64
		f, _ := fetchFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/robots.txt" {
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte(robots))
				return
			}
			pages.Add(1)
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>not permitted</html>"))
		}))
		if _, err := f.Fetch(t.Context(), "https://example.com/private/secret"); !errors.Is(err, ErrRecipeSourceUnavailable) || pages.Load() != 0 {
			t.Fatal("robots refusal was bypassed")
		}
	}
	for _, status := range []int{401, 403, 429, 500} {
		f, _ := fetchFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/robots.txt" {
				w.WriteHeader(404)
				return
			}
			w.WriteHeader(status)
		}))
		if _, err := f.Fetch(t.Context(), "https://example.com/recipe"); !errors.Is(err, ErrRecipeSourceUnavailable) {
			t.Fatal("access refusal or server error was bypassed")
		}
	}
}

func TestRecipeFetchRejectsOversizedUnsupportedAndTruncatedResponses(t *testing.T) {
	for _, mode := range []string{"content type", "wire size", "expanded size", "truncated gzip", "stacked encoding", "invalid utf8"} {
		t.Run(mode, func(t *testing.T) {
			f, connections := fetchFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/robots.txt" {
					w.WriteHeader(404)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				switch mode {
				case "content type":
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = w.Write([]byte("binary"))
				case "wire size":
					_, _ = w.Write(bytes.Repeat([]byte("x"), maxRecipeWireBytes+1))
				case "expanded size":
					w.Header().Set("Content-Encoding", "gzip")
					z := gzip.NewWriter(w)
					_, _ = z.Write(bytes.Repeat([]byte("x"), maxRecipeHTMLBytes+1))
					_ = z.Close()
				case "truncated gzip":
					w.Header().Set("Content-Encoding", "gzip")
					_, _ = w.Write([]byte{31, 139, 8})
				case "stacked encoding":
					w.Header().Set("Content-Encoding", "gzip, br")
					_, _ = w.Write([]byte("not supported"))
				case "invalid utf8":
					_, _ = w.Write([]byte{0xff})
				}
			}))
			if _, err := f.Fetch(t.Context(), "https://example.com/recipe?utm_source=private-canary"); !errors.Is(err, ErrRecipeSourceUnavailable) || strings.Contains(err.Error(), "private-canary") || connections.Load() != 2 {
				t.Fatal("unsafe response accepted or private URL exposed")
			}
		})
	}
}

func TestRecipeFetchStopsOnCancellationAndBoundedDeadline(t *testing.T) {
	f, _ := fetchFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(404)
			return
		}
		<-r.Context().Done()
	}))
	f.timeout = 100 * time.Millisecond
	started := time.Now()
	if _, err := f.Fetch(t.Context(), "https://example.com/slow"); !errors.Is(err, ErrRecipeSourceUnavailable) || time.Since(started) > 2*time.Second {
		t.Fatal("slow source escaped the total deadline")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.Fetch(ctx, "https://example.com/recipe"); !errors.Is(err, ErrRecipeSourceUnavailable) {
		t.Fatal("canceled fetch was attempted")
	}
}
