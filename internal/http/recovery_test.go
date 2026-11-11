package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func recoveryAPI(t *testing.T) (*API, http.Handler, string) {
	t.Helper()
	a, _ := testAPI(t)
	keys, err := mailqueue.NewProofKeys("fixture", map[string][]byte{"fixture": bytes.Repeat([]byte{2}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	a.Config.Recovery = &auth.Recovery{SignUp: &auth.SignUp{
		Store: a.Store, Keys: keys, RateKey: bytes.Repeat([]byte{3}, 32), Origin: a.Config.Origin,
	}, Work: &store.RecoveryWork{}}
	anonymous, err := a.Auth.Rotate(context.Background(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return a, a.Handler(nil), anonymous
}

func TestRecoveryRequestRequiresAnonymousOriginAndHidesAccountState(t *testing.T) {
	ctx := context.Background()
	a, h, anonymous := recoveryAPI(t)
	var sessions []string
	for _, email := range []string{"verified@example.test", "restricted@example.test"} {
		u, err := a.Auth.Register(ctx, auth.Registration{Email: email, Username: email, FirstName: "Test", LastName: "User", Password: "a fixture password for this test"})
		if err != nil {
			t.Fatal(err)
		}
		if email == "verified@example.test" {
			if err := a.Store.DB.Model(&u).Update("email_verified_at", time.Now().Unix()).Error; err != nil {
				t.Fatal(err)
			}
		}
		cookie, err := a.Auth.Rotate(ctx, &u.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, cookie)
	}
	for _, cookie := range sessions {
		for _, path := range []string{requestRecoveryPath, completeRecoveryPath} {
			got := call(h, "POST", path, "{}", cookie, auth.CSRF(cookie), a.Config.Origin)
			if got.Code != 409 || got.Header().Get("Set-Cookie") != "" || got.Header().Get("X-MyChef-User") != "" {
				t.Fatalf("a signed-in request was accepted or disclosed identity: %d", got.Code)
			}
		}
	}
	for _, pair := range [][2]string{{"", auth.CSRF(anonymous)}, {"https://wrong.test", auth.CSRF(anonymous)}, {a.Config.Origin, ""}, {a.Config.Origin, "wrong"}} {
		got := call(h, "POST", completeRecoveryPath, "{}", anonymous, pair[1], pair[0])
		if got.Code != 403 || got.Header().Get("Set-Cookie") != "" {
			t.Fatal("completion accepted a request without the exact origin and CSRF")
		}
	}
	var acknowledgement string
	for _, email := range []string{"verified@example.test", "restricted@example.test", "unknown@example.test"} {
		start := time.Now()
		got := call(h, "POST", requestRecoveryPath, `{"email":"`+email+`"}`, anonymous, auth.CSRF(anonymous), a.Config.Origin)
		if time.Since(start) < 250*time.Millisecond || got.Code != 202 || got.Header().Get("Set-Cookie") != "" ||
			got.Header().Get("Cache-Control") != "no-store" || got.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("request did not use the common protected acknowledgement: %d", got.Code)
		}
		if acknowledgement != "" && got.Body.String() != acknowledgement {
			t.Fatal("the acknowledgement changed by account state")
		}
		acknowledgement = got.Body.String()
	}
}

func TestRecoveryTimingEnvelopeIncludesBlockedSessionLookup(t *testing.T) {
	a, h, anonymous := recoveryAPI(t)
	held, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	backgroundDone := make(chan struct{})
	defer func() { close(release); <-backgroundDone }()
	go func() {
		defer close(backgroundDone)
		_ = a.Config.Recovery.Work.Background(ctx, func() error {
			return a.Store.DB.WithContext(ctx).Transaction(func(_ *gorm.DB) error {
				close(held)
				select {
				case <-release:
				case <-ctx.Done():
				}
				return nil
			})
		})
	}()
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal("could not hold the database connection")
	}
	type result struct {
		code int
		took time.Duration
	}
	done := make(chan result, 1)
	go func() {
		start := time.Now()
		got := call(h, "POST", requestRecoveryPath, `{"email":"target@example.test"}`, anonymous, auth.CSRF(anonymous), a.Config.Origin)
		done <- result{got.Code, time.Since(start)}
	}()
	select {
	case got := <-done:
		if got.code != 503 || got.took < 250*time.Millisecond {
			t.Fatalf("session lookup escaped the request budget or floor: %d, %v", got.code, got.took)
		}
	case <-ctx.Done():
		t.Fatal("the guard waited outside the request budget")
	}
}

func TestRecoveryCompletionChargesSourceEvenWhenTokenIsBlocked(t *testing.T) {
	a, h, anonymous := recoveryAPI(t)
	for i := range 50 {
		got := call(h, "POST", completeRecoveryPath, `{"proof":"malformed","password":"fixture password for the request"}`, anonymous, auth.CSRF(anonymous), a.Config.Origin)
		want := 400
		if i >= 10 {
			want = 429
		}
		if got.Code != want {
			t.Fatalf("attempt %d: got %d, want %d", i+1, got.Code, want)
		}
	}
	got := call(h, "POST", completeRecoveryPath, `{"proof":"different","password":"fixture password for the request"}`, anonymous, auth.CSRF(anonymous), a.Config.Origin)
	if got.Code != 429 || got.Header().Get("Retry-After") == "" {
		t.Fatal("token-blocked submissions did not exhaust the source allowance")
	}
	var count int
	if err := a.Store.SQL.QueryRow("SELECT count(*) FROM email_rate_events").Scan(&count); err != nil || count != 60 {
		t.Fatalf("denied request unexpectedly changed counters: %d (%v)", count, err)
	}
}

func TestRecoveryCompletionChargesMalformedRequestsWithoutDoingPasswordWork(t *testing.T) {
	a, h, anonymous := recoveryAPI(t)
	proof := "a bounded proof with a malformed password field"
	for i, payload := range []string{`{"proof":`, `{"proof":null,"password":"unused"}`, `{"proof":"` + strings.Repeat("a", maxRecoveryProofBytes+1) + `","password":"unused"}`, `{"proof":"` + proof + `","password":false}`} {
		got := call(h, "POST", completeRecoveryPath, payload, anonymous, auth.CSRF(anonymous), a.Config.Origin)
		if got.Code != 400 {
			t.Fatalf("malformed submission %d: got %d", i, got.Code)
		}
	}
	var count int
	if err := a.Store.SQL.QueryRow("SELECT count(*) FROM email_rate_events").Scan(&count); err != nil || count != 5 {
		t.Fatalf("want four source events and only one proof event: %d (%v)", count, err)
	}
	bucket, err := a.Config.Recovery.RecoveryCompletionBucket(proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SQL.QueryRow("SELECT count(*) FROM email_rate_events WHERE bucket=?", bucket).Scan(&count); err != nil || count != 1 {
		t.Fatalf("bounded proof did not count alongside malformed password: %d (%v)", count, err)
	}
}

func TestRecoveryCompletionClearsOnlyProofAndDoesNotSignIn(t *testing.T) {
	a, h, anonymous := recoveryAPI(t)
	recovery := a.Config.Recovery
	now := time.Now().UTC()
	recovery.Now = func() time.Time { return now }
	owner := verifiedRecoveryOwner(t, a, now)
	requested := call(h, "POST", requestRecoveryPath, `{"email":"VERIFIED@example.test"}`, anonymous, auth.CSRF(anonymous), a.Config.Origin)
	if requested.Code != 202 {
		t.Fatalf("request failed: %d", requested.Code)
	}
	proof := readRecoveryFixtureProof(t, a, owner, now)
	complete := func(proof, password string) *http.Response {
		return call(h, "POST", completeRecoveryPath, `{"proof":"`+proof+`","password":"`+password+`"}`, anonymous, auth.CSRF(anonymous), a.Config.Origin).Result()
	}
	if failed := complete(proof, "short"); failed.StatusCode != 400 {
		t.Fatalf("password policy not enforced: %d", failed.StatusCode)
	}
	bucket, err := recovery.RecoveryCompletionBucket(proof)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.EmailRateBucket(recovery.RateKey, "recovery-completion/source", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	check := func(bucket string, want int) {
		t.Helper()
		var count int
		if err := a.Store.SQL.QueryRow("SELECT count(*) FROM email_rate_events WHERE bucket=?", bucket).Scan(&count); err != nil || count != want {
			t.Fatalf("unexpected reservation count: got %d, want %d (%v)", count, want, err)
		}
	}
	check(bucket, 1)
	password := "a different fixture password after reset"
	result := complete(proof, password)
	if result.StatusCode != 200 || result.Header.Get("Set-Cookie") != "" || result.Header.Get("X-MyChef-User") != "" {
		t.Fatalf("reset failed or created a sign-in session: %d", result.StatusCode)
	}
	check(bucket, 0)
	check(source, 2)
	replay := call(h, "POST", completeRecoveryPath, `{"proof":"`+proof+`","password":"`+password+`"}`, anonymous, auth.CSRF(anonymous), a.Config.Origin)
	random := call(h, "POST", completeRecoveryPath, `{"proof":"`+strings.Repeat("a", 43)+`","password":"`+password+`"}`, anonymous, auth.CSRF(anonymous), a.Config.Origin)
	// Error envelopes have different correlation IDs; compare the public guidance.
	if replay.Code != 400 || random.Code != 400 || !strings.Contains(replay.Body.String(), "invalid_link") || !strings.Contains(random.Body.String(), "invalid_link") ||
		strings.Contains(replay.Body.String(), proof) {
		t.Fatal("a used proof was distinguishable from an invalid link")
	}
	check(bucket, 1)
	check(source, 4)
}

func verifiedRecoveryOwner(t *testing.T, a *API, now time.Time) store.User {
	t.Helper()
	ctx := t.Context()
	owner, err := a.Auth.Register(ctx, auth.Registration{Email: "verified@example.test", Username: "verified", FirstName: "Test", LastName: "User", Password: "a fixture password for this test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&owner).Update("email_verified_at", now.Unix()).Error; err != nil {
			return err
		}
		_, err := store.AdvanceRecoveryFence(ctx, tx, owner.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return owner
}

func readRecoveryFixtureProof(t *testing.T, a *API, owner store.User, now time.Time) string {
	t.Helper()
	recovery := a.Config.Recovery
	if worked, err := recovery.Step(t.Context()); !worked || err != nil {
		t.Fatalf("could not issue recovery link: %v", err)
	}
	var row mailqueue.Record
	if err := a.Store.DB.Where("template = ?", mailqueue.RecoveryProofTemplate).Order("id DESC").Take(&row).Error; err != nil || row.Recipient != owner.Email {
		t.Fatal("missing protected mail for exact verified address")
	}
	fields, _, err := mailqueue.OpenProtected(recovery.Keys, row, now, false)
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(fields["link"])
	if err != nil || link.Fragment == "" || link.Path != "/app/reset-password" || link.RawQuery != "" {
		t.Fatal("missing fragment-only proof")
	}
	return link.Fragment
}

func TestRecoveryInvalidProofsGiveIdenticalGuidanceAndLeavePasswordUnchanged(t *testing.T) {
	a, h, anonymous := recoveryAPI(t)
	r := a.Config.Recovery
	now := time.Now().UTC()
	r.Now = func() time.Time { return now }
	owner := verifiedRecoveryOwner(t, a, now)
	issue := func() string {
		t.Helper()
		if wait, err := r.Request(t.Context(), "192.0.2.5", owner.Email); err != nil || wait != 0 {
			t.Fatal("could not stage recovery mail")
		}
		return readRecoveryFixtureProof(t, a, owner, now)
	}
	superseded, expired := issue(), issue()
	var guidance [2]string
	for i, proof := range []string{superseded, expired, "malformed", base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x43}, 32))} {
		if i == 1 {
			now = now.Add(30 * time.Minute)
		}
		response := call(h, "POST", completeRecoveryPath, `{"proof":"`+proof+`","password":"another fixture password"}`, anonymous, auth.CSRF(anonymous), a.Config.Origin)
		var envelope struct {
			Error struct {
				Code, Message string
			}
		}
		if response.Code != 400 || response.Header().Get("Set-Cookie") != "" ||
			json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Error.Code != "invalid_link" ||
			strings.Contains(response.Body.String(), proof) {
			t.Fatalf("invalid proof state %d had a different response", i)
		}
		current := [2]string{envelope.Error.Code, envelope.Error.Message}
		if i != 0 && current != guidance {
			t.Fatal("invalid proof guidance changed across states")
		}
		guidance = current
		var persisted store.User
		if err := a.Store.DB.First(&persisted, owner.ID).Error; err != nil || persisted.Password != owner.Password {
			t.Fatal("invalid proof changed account credentials")
		}
	}
}
