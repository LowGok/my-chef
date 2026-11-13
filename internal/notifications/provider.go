package notifications

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const pushTitle = "Plan your meals"
const pushMessage = "Ready to plan your week?"

type ApplePush struct {
	Team, KeyID, Topic, PrivateKey, Environment string
}

type GooglePush struct {
	Project, Email, PrivateKey string
}

type PushSender struct {
	topic, project, appleHost string
	client                    *http.Client
	credential                func(context.Context, string) (string, error)
	now                       func() time.Time
	apple, google             bool
}

type cachedPushCredential struct {
	value string
	until time.Time
}

var pushID = regexp.MustCompile(`^[A-Z0-9]{10}$`)
var pushTopic = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{2,150}$`)
var pushProject = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
var pushReference = regexp.MustCompile(`^[0-9a-f]{32}$`)
var pushToken = regexp.MustCompile(`^[A-Za-z0-9_:-]{32,512}$`)
var errPushConfig = errors.New("invalid push sender configuration")

func NewPushSender(apple *ApplePush, google *GooglePush) (*PushSender, error) {
	if apple == nil && google == nil {
		return nil, errPushConfig
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext:       (&net.Dialer{Timeout: 4 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2: true, TLSHandshakeTimeout: 5 * time.Second,
			MaxIdleConns: 8, IdleConnTimeout: 60 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	s := &PushSender{client: client, now: time.Now}
	var appleKey *ecdsa.PrivateKey
	var googleKey *rsa.PrivateKey
	if apple != nil {
		if !pushID.MatchString(apple.Team) || !pushID.MatchString(apple.KeyID) || !pushTopic.MatchString(apple.Topic) {
			return nil, errPushConfig
		}
		s.appleHost = "https://api.push.apple.com"
		if apple.Environment == "development" {
			s.appleHost = "https://api.sandbox.push.apple.com"
		} else if apple.Environment != "production" {
			return nil, errPushConfig
		}
		key, err := parsePushKey(apple.PrivateKey)
		var ok bool
		appleKey, ok = key.(*ecdsa.PrivateKey)
		if err != nil || !ok || appleKey.Curve.Params().BitSize != 256 {
			return nil, errPushConfig
		}
		s.topic, s.apple = apple.Topic, true
	}
	if google != nil {
		if !pushProject.MatchString(google.Project) || !strings.HasSuffix(google.Email, "@"+google.Project+".iam.gserviceaccount.com") || len(google.Email) > 250 {
			return nil, errPushConfig
		}
		key, err := parsePushKey(google.PrivateKey)
		var ok bool
		googleKey, ok = key.(*rsa.PrivateKey)
		if err != nil || !ok || googleKey.N.BitLen() < 2048 {
			return nil, errPushConfig
		}
		s.project, s.google = google.Project, true
	}
	var mu sync.Mutex
	cache := map[string]cachedPushCredential{}
	s.credential = func(ctx context.Context, provider string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		at := s.now()
		if existing := cache[provider]; at.Before(existing.until) {
			return existing.value, nil
		}
		var entry cachedPushCredential
		var err error
		switch {
		case provider == "apns" && appleKey != nil:
			entry.value, err = pushJWT(map[string]any{"alg": "ES256", "kid": apple.KeyID}, map[string]any{"iss": apple.Team, "iat": at.Unix()}, appleKey)
			entry.until = at.Add(45 * time.Minute)
		case provider == "fcm" && googleKey != nil:
			entry, err = googleAccess(ctx, client, google.Email, googleKey, at)
		default:
			return "", errPushConfig
		}
		if err == nil {
			cache[provider] = entry
		}
		return entry.value, err
	}
	return s, nil
}

func parsePushKey(value string) (crypto.PrivateKey, error) {
	if len(value) > 16<<10 {
		return nil, errPushConfig
	}
	block, rest := pem.Decode([]byte(value))
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errPushConfig
	}
	return x509.ParsePKCS8PrivateKey(block.Bytes)
}

func pushJWT(header, claims map[string]any, key crypto.PrivateKey) (string, error) {
	encode := base64.RawURLEncoding.EncodeToString
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	c, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	body := encode(h) + "." + encode(c)
	digest := sha256.Sum256([]byte(body))
	var signature []byte
	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		r, s, e := ecdsa.Sign(rand.Reader, k, digest[:])
		if e != nil {
			return "", e
		}
		signature = make([]byte, 64)
		r.FillBytes(signature[:32])
		s.FillBytes(signature[32:])
	case *rsa.PrivateKey:
		signature, err = rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, digest[:])
	default:
		return "", errPushConfig
	}
	if err != nil {
		return "", err
	}
	return body + "." + encode(signature), nil
}

func googleAccess(ctx context.Context, client *http.Client, email string, key *rsa.PrivateKey, now time.Time) (cachedPushCredential, error) {
	const endpoint = "https://oauth2.googleapis.com/token"
	jwt, err := pushJWT(map[string]any{"alg": "RS256", "typ": "JWT"}, map[string]any{
		"iss": email, "aud": endpoint, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		"scope": "https://www.googleapis.com/auth/firebase.messaging",
	}, key)
	if err != nil {
		return cachedPushCredential{}, err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {jwt}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return cachedPushCredential{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return cachedPushCredential{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var reply struct {
		Access  string `json:"access_token"`
		Seconds int    `json:"expires_in"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&reply) != nil ||
		len(reply.Access) < 16 || len(reply.Access) > 4096 || reply.Seconds < 120 || reply.Seconds > 3600 {
		return cachedPushCredential{}, errPushConfig
	}
	return cachedPushCredential{value: reply.Access, until: now.Add(time.Duration(reply.Seconds-60) * time.Second)}, nil
}

func (s *PushSender) Supports(provider string) bool {
	return provider == "apns" && s.apple || provider == "fcm" && s.google
}

func (s *PushSender) Send(ctx context.Context, d Delivery) Result {
	if !pushReference.MatchString(d.Reference) || !pushToken.MatchString(d.Token) || !s.now().Before(d.Deadline) {
		return Result{State: Permanent}
	}
	access, err := s.credential(ctx, d.Provider)
	if err != nil { // There was no push submission.
		return Result{State: Permanent}
	}
	at := s.now()
	ttl := min(int64(d.Deadline.Sub(at)/time.Second), 86400)
	if ttl <= 0 {
		return Result{State: Permanent}
	}
	var endpoint string
	var body any
	switch d.Provider {
	case "apns":
		host := s.appleHost
		if host == "" {
			host = "https://api.push.apple.com"
		}
		endpoint = host + "/3/device/" + d.Token
		body = map[string]any{"aps": map[string]any{"alert": map[string]string{"title": pushTitle, "body": pushMessage}, "sound": "default"}, "reminder": d.Reference}
	case "fcm":
		endpoint = "https://fcm.googleapis.com/v1/projects/" + s.project + "/messages:send"
		body = map[string]any{"message": map[string]any{
			"token": d.Token, "notification": map[string]string{"title": pushTitle, "body": pushMessage},
			"data":    map[string]string{"reminder": d.Reference},
			"android": map[string]any{"ttl": strconv.FormatInt(ttl, 10) + "s", "notification": map[string]string{"tag": d.Reference, "channel_id": "mychef-planning"}},
		}}
	default:
		return Result{State: Permanent}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Result{State: Permanent}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, io.NopCloser(bytes.NewReader(payload)))
	if err != nil {
		return Result{State: Permanent}
	}
	// No replayable body or idempotency header; no client redirect or SDK retry.
	req.ContentLength = int64(len(payload))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	if d.Provider == "apns" {
		req.Header.Set("apns-topic", s.topic)
		req.Header.Set("apns-expiration", strconv.FormatInt(d.Deadline.Unix(), 10))
		req.Header.Set("apns-push-type", "alert")
		req.Header.Set("apns-collapse-id", d.Reference)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Result{State: Uncertain}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK && d.Provider == "apns" {
		return Result{State: Accepted}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if err != nil || len(data) > 8192 {
		return Result{State: Uncertain}
	}
	if resp.StatusCode == http.StatusOK {
		var accepted struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &accepted) != nil {
			return Result{State: Uncertain}
		}
		parts := strings.Split(accepted.Name, "/")
		if len(parts) != 4 || parts[0] != "projects" || parts[1] == "" || parts[2] != "messages" || parts[3] == "" {
			return Result{State: Uncertain}
		}
		return Result{State: Accepted}
	}
	var reply struct {
		Reason string `json:"reason"`
		Error  struct {
			Status  string `json:"status"`
			Details []struct {
				Type string `json:"@type"`
				Code string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &reply) != nil {
		return Result{State: Uncertain}
	}
	delay := retryAfter(resp.Header.Get("Retry-After"), at)
	if d.Provider == "apns" {
		switch {
		case resp.StatusCode == 429 && reply.Reason == "TooManyRequests":
			return Result{State: Temporary, RetryAfter: delay}
		case resp.StatusCode == 503 && (reply.Reason == "Shutdown" || reply.Reason == "ServiceUnavailable" || reply.Reason == "Service Unavailable"):
			return Result{State: Temporary, RetryAfter: max(delay, 15*time.Minute)}
		case resp.StatusCode == 410 && reply.Reason == "Unregistered", resp.StatusCode == 400 && reply.Reason == "BadDeviceToken":
			return Result{State: InvalidRegistration}
		case resp.StatusCode >= 400 && resp.StatusCode < 500 && reply.Reason != "":
			return Result{State: Permanent}
		}
	} else {
		switch {
		case resp.StatusCode == 429 && reply.Error.Status == "RESOURCE_EXHAUSTED", resp.StatusCode == 503 && reply.Error.Status == "UNAVAILABLE":
			return Result{State: Temporary, RetryAfter: max(delay, time.Minute)}
		}
		if resp.StatusCode == 404 {
			for _, detail := range reply.Error.Details {
				if detail.Type == "type.googleapis.com/google.firebase.fcm.v1.FcmError" && detail.Code == "UNREGISTERED" {
					return Result{State: InvalidRegistration}
				}
			}
		}
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && reply.Error.Status != "" {
			return Result{State: Permanent}
		}
	}
	return Result{State: Uncertain}
}

func retryAfter(value string, now time.Time) time.Duration {
	if secs, err := strconv.ParseInt(value, 10, 32); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(0, date.Sub(now))
	}
	return 0
}
