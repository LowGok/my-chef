package imports

import (
	"compress/gzip"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestRecipeRobotsGroupsPathsAndEncoding(t *testing.T) {
	cases := []struct {
		name, text, path string
		allowed          bool
	}{
		{"longest allow", "User-agent: *\nDisallow: /private\nAllow: /private/recipe\n", "/private/recipe", true},
		{"exact group", "User-agent: MyChef\nAllow: /\nUser-agent: *\nDisallow: /\n", "/recipe", true},
		{"empty matching group", "User-agent: MyChef\nDisallow:\nUser-agent: OtherBot\nDisallow: /\n", "/recipe", true},
		{"merged matching groups", "User-agent: MyChef\nDisallow: /one\nUser-agent: MyChef\nDisallow: /two\n", "/two", false},
		{"percent unreserved", "User-agent: *\nDisallow: /%72ecipe\n", "/recipe", false},
		{"encoded reserved", "User-agent: *\nDisallow: /one%2Ftwo\n", "/one%2ftwo", false},
		{"reserved distinct", "User-agent: *\nDisallow: /one%2Ftwo\n", "/one/two", true},
		{"wildcard priority", "User-agent: *\nDisallow: /recipe*\nAllow: /recipe\n", "/recipe/extra", false},
		{"end marker priority", "User-agent: *\nDisallow: /recipe$\nAllow: /recipe\n", "/recipe", false},
		{"literal marker encoding", "User-agent: *\nDisallow: /recipe%24\n", "/recipe$", false},
		{"allow tie", "User-agent: *\nDisallow: /recipe\nAllow: /recipe\n", "/recipe", true},
		{"UTF8 BOM", "\ufeffUser-agent: *\nDisallow: /private\n", "/private", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := parseRecipeRobots([]byte(tc.text))
			if err != nil || policy.allowed(tc.path) != tc.allowed {
				t.Fatal("robots policy does not preserve source restrictions")
			}
		})
	}
	for _, text := range []string{strings.Repeat("x", maxRobotsBytes+1), "User-agent: *\nDisallow: not-a-path\n", "User-agent: *\n" + strings.Repeat("Disallow: /\n", 257)} {
		if _, err := parseRecipeRobots([]byte(text)); !errors.Is(err, ErrRecipeSourceUnavailable) {
			t.Fatal("unbounded or malformed robots document accepted")
		}
	}
}

func TestRecipeFetchNeverSubmitsCredentialQueries(t *testing.T) {
	f, connections := fetchFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>private recipe</html>"))
	}))
	if _, err := f.Fetch(t.Context(), "https://example.com/recipe?access_token=private-canary"); !errors.Is(err, ErrRecipeSourceUnavailable) || connections.Load() != 0 {
		t.Fatal("credential-bearing recipe URL reached the network")
	}
}

func TestRecipeFetchAllowsPermittedRobotsAndValidGzip(t *testing.T) {
	f, connections := fetchFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private\nAllow: /private/recipe\n"))
			return
		}
		if r.URL.RawQuery != "" {
			t.Error("tracking data was forwarded to the source")
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Encoding", "gzip")
		z := gzip.NewWriter(w)
		_, _ = z.Write([]byte("<html>Allowed written recipe.</html>"))
		_ = z.Close()
	}))
	page, err := f.Fetch(t.Context(), "https://example.com/private/recipe?utm_source=private-canary")
	if err != nil || string(page.HTML) != "<html>Allowed written recipe.</html>" || connections.Load() != 2 {
		t.Fatal("permitted compressed recipe rejected")
	}
}

func TestRecipeFetchRedirectCannotReachPrivateOrDowngradeTargets(t *testing.T) {
	for _, target := range []string{"https://169.254.169.254/metadata", "http://example.com/recipe", "https://user:secret@example.com/recipe"} {
		f, connections := fetchFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/robots.txt" {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Location", target)
			w.WriteHeader(302)
		}))
		if _, err := f.Fetch(t.Context(), "https://example.com/start"); !errors.Is(err, ErrRecipeSourceUnavailable) || connections.Load() != 2 {
			t.Fatal("redirect escaped the HTTPS/public-target boundary")
		}
	}
}

func TestRecipeFetchDoesNotDisableTLSVerification(t *testing.T) {
	f, connections := fetchFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS server received an HTTP request") }))
	f.roots = nil
	if _, err := f.Fetch(t.Context(), "https://example.com/recipe"); !errors.Is(err, ErrRecipeSourceUnavailable) || connections.Load() != 1 {
		t.Fatal("untrusted TLS identity was accepted")
	}
}

func TestImportSourceBoundsCanonicalEncoding(t *testing.T) {
	source := sourceFixture()
	source.URL = "https://recipes.example.com/" + strings.Repeat("é", 1500)
	if _, err := NewDraft(extractionFixture(), source); !errors.Is(err, ErrImportInput) {
		t.Fatal("canonical source URL grew beyond its bound")
	}
}
