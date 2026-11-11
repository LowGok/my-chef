package imports

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrRecipeSourceUnavailable = errors.New("that recipe page is unavailable; enter the recipe manually")

const (
	recipeImportAgent  = "MyChef/1.0 (recipe import)"
	maxRecipeWireBytes = 2 << 20
	maxRecipeHTMLBytes = 8 << 20
	maxRobotsBytes     = 128 << 10
	maxFetchRequests   = 12
)

type fetchResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// Fetcher has dedicated DNS, connection and TLS settings. It never uses proxy
// environment variables, a cookie jar, browser headers or a caller's client.
type Fetcher struct {
	resolver fetchResolver
	dial     func(context.Context, string, string) (net.Conn, error)
	roots    *x509.CertPool
	timeout  time.Duration
}

type RecipePage struct {
	URL  string
	HTML []byte
}

type fetchAttempt struct {
	requests, redirects int
	robots              map[string]robotsPolicy
}

type sourceResponse struct {
	status   int
	location string
	body     []byte
}

func NewFetcher() *Fetcher {
	dialer := &net.Dialer{Timeout: 3 * time.Second, KeepAlive: -1}
	return &Fetcher{resolver: net.DefaultResolver, dial: dialer.DialContext, timeout: 20 * time.Second}
}

func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (RecipePage, error) {
	if f == nil || f.resolver == nil || f.dial == nil || ctx == nil || ctx.Err() != nil || f.timeout <= 0 || f.timeout > 20*time.Second {
		return RecipePage{}, ErrRecipeSourceUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	current, err := checkedRecipeURL(rawURL)
	if err != nil {
		return RecipePage{}, ErrRecipeSourceUnavailable
	}
	attempt := &fetchAttempt{robots: make(map[string]robotsPolicy)}
	for {
		policy, err := f.readRobots(ctx, current, attempt)
		if err != nil || !policy.allowed(current.RequestURI()) {
			return RecipePage{}, ErrRecipeSourceUnavailable
		}
		response, err := f.request(ctx, current, false, attempt)
		if err != nil {
			return RecipePage{}, ErrRecipeSourceUnavailable
		}
		if isRecipeRedirect(response.status) {
			current, err = nextRecipeURL(current, response.location, attempt)
			if err != nil {
				return RecipePage{}, ErrRecipeSourceUnavailable
			}
			continue
		}
		if response.status != http.StatusOK || len(response.body) == 0 || !utf8.Valid(response.body) {
			return RecipePage{}, ErrRecipeSourceUnavailable
		}
		return RecipePage{URL: current.String(), HTML: response.body}, nil
	}
}

func (f *Fetcher) request(ctx context.Context, target *url.URL, robots bool, attempt *fetchAttempt) (sourceResponse, error) {
	if ctx.Err() != nil || attempt.requests >= maxFetchRequests {
		return sourceResponse{}, ErrRecipeSourceUnavailable
	}
	attempt.requests++
	addresses, err := f.publicAddresses(ctx, target.Hostname())
	if err != nil {
		return sourceResponse{}, ErrRecipeSourceUnavailable
	}
	pinned := addresses[0]
	transport := &http.Transport{
		Proxy: nil, DisableCompression: true, DisableKeepAlives: true,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: f.roots},
		TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 5 * time.Second,
		MaxResponseHeaderBytes: 16 << 10, ForceAttemptHTTP2: false,
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
		DialContext: func(call context.Context, network, address string) (net.Conn, error) {
			if address != net.JoinHostPort(target.Hostname(), "443") {
				return nil, ErrRecipeSourceUnavailable
			}
			conn, err := f.dial(call, network, net.JoinHostPort(pinned.String(), "443"))
			if err != nil {
				return nil, ErrRecipeSourceUnavailable
			}
			peer, err := netip.ParseAddrPort(conn.RemoteAddr().String())
			if err != nil || peer.Port() != 443 || peer.Addr().Unmap() != pinned.Unmap() {
				_ = conn.Close()
				return nil, ErrRecipeSourceUnavailable
			}
			return conn, nil
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return sourceResponse{}, ErrRecipeSourceUnavailable
	}
	request.Header.Set("User-Agent", recipeImportAgent)
	request.Header.Set("Accept-Encoding", "gzip")
	request.Header.Set("Accept", "text/html")
	if robots {
		request.Header.Set("Accept", "text/plain")
	}
	response, err := client.Do(request)
	if err != nil {
		return sourceResponse{}, ErrRecipeSourceUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	result := sourceResponse{status: response.StatusCode}
	if isRecipeRedirect(response.StatusCode) {
		result.location = response.Header.Get("Location")
		if result.location == "" || len(result.location) > 4096 {
			return sourceResponse{}, ErrRecipeSourceUnavailable
		}
		return result, nil
	}
	if response.StatusCode != http.StatusOK {
		return result, nil
	}
	media, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || (!robots && media != "text/html") || (robots && media != "text/plain") {
		return sourceResponse{}, ErrRecipeSourceUnavailable
	}
	if charset := strings.ToLower(parameters["charset"]); charset != "" && charset != "utf-8" && charset != "us-ascii" {
		return sourceResponse{}, ErrRecipeSourceUnavailable
	}
	wireLimit, decodedLimit := int64(maxRecipeWireBytes), int64(maxRecipeHTMLBytes)
	if robots {
		wireLimit, decodedLimit = maxRobotsBytes, maxRobotsBytes
	}
	if response.ContentLength > wireLimit {
		return sourceResponse{}, ErrRecipeSourceUnavailable
	}
	wire, err := io.ReadAll(io.LimitReader(response.Body, wireLimit+1))
	if err != nil || int64(len(wire)) > wireLimit {
		return sourceResponse{}, ErrRecipeSourceUnavailable
	}
	encoding := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding")))
	switch encoding {
	case "", "identity":
		result.body = wire
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(wire))
		if err != nil {
			return sourceResponse{}, ErrRecipeSourceUnavailable
		}
		decoded, err := io.ReadAll(io.LimitReader(reader, decodedLimit+1))
		_ = reader.Close()
		if err != nil || int64(len(decoded)) > decodedLimit {
			return sourceResponse{}, ErrRecipeSourceUnavailable
		}
		result.body = decoded
	default:
		return sourceResponse{}, ErrRecipeSourceUnavailable
	}
	return result, nil
}

func (f *Fetcher) publicAddresses(ctx context.Context, host string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		if !publicRecipeAddress(address) {
			return nil, ErrRecipeSourceUnavailable
		}
		return []netip.Addr{address.Unmap()}, nil
	}
	lookup, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addresses, err := f.resolver.LookupNetIP(lookup, "ip", host)
	if err != nil || len(addresses) == 0 || len(addresses) > 8 {
		return nil, ErrRecipeSourceUnavailable
	}
	for i, address := range addresses {
		if !publicRecipeAddress(address) {
			return nil, ErrRecipeSourceUnavailable
		}
		addresses[i] = address.Unmap()
	}
	return addresses, nil
}

var recipeBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
}

func publicRecipeAddress(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || (address.Is6() && !netip.MustParsePrefix("2000::/3").Contains(address)) {
		return false
	}
	for _, prefix := range recipeBlockedPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

var recipeHostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func checkedRecipeURL(raw string) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > 4096 || !safeDraftText(raw) || strings.ContainsAny(raw, "\\\r\n\t") {
		return nil, ErrRecipeSourceUnavailable
	}
	u, err := normalizedImportURL(raw)
	if err != nil || strings.ToLower(u.Scheme) != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || (u.Port() != "" && u.Port() != "443") {
		return nil, ErrRecipeSourceUnavailable
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if address, err := netip.ParseAddr(host); err == nil {
		if !publicRecipeAddress(address) {
			return nil, ErrRecipeSourceUnavailable
		}
		host = address.Unmap().String()
	} else {
		if len(host) > 253 || !strings.Contains(host, ".") {
			return nil, ErrRecipeSourceUnavailable
		}
		labels := strings.Split(host, ".")
		for _, label := range labels {
			if !recipeHostLabel.MatchString(label) {
				return nil, ErrRecipeSourceUnavailable
			}
		}
		last := labels[len(labels)-1]
		if !strings.ContainsAny(last, "abcdefghijklmnopqrstuvwxyz") {
			return nil, ErrRecipeSourceUnavailable
		}
		for _, suffix := range []string{".local", ".localhost", ".internal", ".test", ".invalid", ".example", ".onion"} {
			if strings.HasSuffix(host, suffix) {
				return nil, ErrRecipeSourceUnavailable
			}
		}
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return nil, ErrRecipeSourceUnavailable
		}
	}
	if strings.ContainsAny(u.Path, "\\\x00\r\n") || strings.Contains(strings.ToLower(u.EscapedPath()), "%25") {
		return nil, ErrRecipeSourceUnavailable
	}
	u.Scheme, u.Host = "https", host
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	u.Fragment, u.RawFragment = "", ""
	return u, nil
}

func isRecipeRedirect(status int) bool {
	return status == 301 || status == 302 || status == 303 || status == 307 || status == 308
}

func nextRecipeURL(current *url.URL, location string, attempt *fetchAttempt) (*url.URL, error) {
	if attempt.redirects >= 3 {
		return nil, ErrRecipeSourceUnavailable
	}
	attempt.redirects++
	next, err := url.Parse(location)
	if err != nil {
		return nil, ErrRecipeSourceUnavailable
	}
	return checkedRecipeURL(current.ResolveReference(next).String())
}

type robotRule struct {
	pattern  *regexp.Regexp
	specific int
	allow    bool
}

type robotsPolicy struct{ rules []robotRule }

func (f *Fetcher) readRobots(ctx context.Context, target *url.URL, attempt *fetchAttempt) (robotsPolicy, error) {
	origin := target.Scheme + "://" + target.Host
	if cached, exists := attempt.robots[origin]; exists {
		return cached, nil
	}
	current, _ := url.Parse(origin + "/robots.txt")
	for {
		response, err := f.request(ctx, current, true, attempt)
		if err != nil {
			return robotsPolicy{}, ErrRecipeSourceUnavailable
		}
		if isRecipeRedirect(response.status) {
			current, err = nextRecipeURL(current, response.location, attempt)
			if err != nil {
				return robotsPolicy{}, ErrRecipeSourceUnavailable
			}
			continue
		}
		if response.status == 404 || response.status == 410 {
			attempt.robots[origin] = robotsPolicy{}
			return robotsPolicy{}, nil
		}
		if response.status != 200 {
			return robotsPolicy{}, ErrRecipeSourceUnavailable
		}
		policy, err := parseRecipeRobots(response.body)
		if err != nil {
			return robotsPolicy{}, ErrRecipeSourceUnavailable
		}
		attempt.robots[origin] = policy
		return policy, nil
	}
}

func parseRecipeRobots(data []byte) (robotsPolicy, error) {
	if !utf8.Valid(data) || len(data) > maxRobotsBytes {
		return robotsPolicy{}, ErrRecipeSourceUnavailable
	}
	type group struct {
		agents []string
		rules  []robotRule
	}
	groups := []group{}
	var current group
	sawRule := false
	flush := func() {
		if len(current.agents) > 0 {
			groups = append(groups, current)
		}
		current = group{}
		sawRule = false
	}
	count := 0
	for _, raw := range strings.Split(strings.ReplaceAll(strings.TrimPrefix(string(data), "\ufeff"), "\r", "\n"), "\n") {
		if len(raw) > 4096 || len(groups) > 256 || count > 256 {
			return robotsPolicy{}, ErrRecipeSourceUnavailable
		}
		line, _, _ := strings.Cut(raw, "#")
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		if key == "user-agent" {
			if sawRule {
				flush()
			}
			if len(current.agents) >= 256 {
				return robotsPolicy{}, ErrRecipeSourceUnavailable
			}
			current.agents = append(current.agents, strings.ToLower(value))
			continue
		}
		if (key != "allow" && key != "disallow") || len(current.agents) == 0 {
			continue
		}
		sawRule = true
		if value == "" {
			continue
		}
		if count >= 256 {
			return robotsPolicy{}, ErrRecipeSourceUnavailable
		}
		if !strings.HasPrefix(value, "/") || len(value) > 2048 {
			return robotsPolicy{}, ErrRecipeSourceUnavailable
		}
		pattern, ok := normalizeRobotPath(value)
		if !ok {
			return robotsPolicy{}, ErrRecipeSourceUnavailable
		}
		specific := len(pattern)
		anchored := strings.HasSuffix(pattern, "$")
		if anchored {
			pattern = strings.TrimSuffix(pattern, "$")
		}
		expression := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*")
		if anchored {
			expression += "$"
		}
		compiled, err := regexp.Compile(expression)
		if err != nil {
			return robotsPolicy{}, ErrRecipeSourceUnavailable
		}
		current.rules = append(current.rules, robotRule{pattern: compiled, specific: specific, allow: key == "allow"})
		count++
	}
	flush()
	if len(groups) > 256 {
		return robotsPolicy{}, ErrRecipeSourceUnavailable
	}
	var matching, fallback []robotRule
	matched := false
	for _, group := range groups {
		for _, agent := range group.agents {
			if agent == "mychef" {
				matching = append(matching, group.rules...)
				matched = true
				break
			}
		}
		for _, agent := range group.agents {
			if agent == "*" {
				fallback = append(fallback, group.rules...)
				break
			}
		}
	}
	if matched {
		return robotsPolicy{rules: matching}, nil
	}
	return robotsPolicy{rules: fallback}, nil
}

func (p robotsPolicy) allowed(path string) bool {
	path = strings.ReplaceAll(strings.ReplaceAll(path, "*", "%2A"), "$", "%24")
	path, ok := normalizeRobotPath(path)
	if !ok {
		return false
	}
	allowed, longest := true, -1
	for _, rule := range p.rules {
		if rule.pattern.MatchString(path) && (rule.specific > longest || (rule.specific == longest && rule.allow)) {
			allowed, longest = rule.allow, rule.specific
		}
	}
	return allowed
}

func normalizeRobotPath(value string) (string, bool) {
	var out strings.Builder
	const hex = "0123456789ABCDEF"
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b == '%' {
			if i+2 >= len(value) {
				return "", false
			}
			decoded, err := url.PathUnescape(value[i : i+3])
			if err != nil || len(decoded) != 1 {
				return "", false
			}
			b = decoded[0]
			if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || strings.ContainsRune("-._~", rune(b)) {
				out.WriteByte(b)
			} else {
				out.WriteByte('%')
				out.WriteByte(hex[b>>4])
				out.WriteByte(hex[b&15])
			}
			i += 2
		} else if b >= 128 || b <= ' ' {
			out.WriteByte('%')
			out.WriteByte(hex[b>>4])
			out.WriteByte(hex[b&15])
		} else {
			out.WriteByte(b)
		}
	}
	return out.String(), true
}
