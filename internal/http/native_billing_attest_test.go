package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/billing"
)

type appleAttestationFixture struct {
	owner, family  string
	challenge, key [32]byte
	beforeCancel   func()
}

func TestAppleLaunchAuthorizationExpiryAndBinding(t *testing.T) {
	issued := time.Unix(2_000_000_000, 0)
	key := [32]byte{1}
	claim := billing.NativeCancellation{
		Attempt: billing.Attempt{ID: "checkout", Binding: "binding", Interval: billing.Monthly, Environment: billing.Sandbox},
		Owner:   "owner", Family: "family", AppInstance: [32]byte{4},
	}
	var wire [appleLaunchAuthorizationBytes]byte
	wire[0] = 7
	binary.BigEndian.PutUint64(wire[32:40], uint64(issued.Unix()))
	copy(wire[40:], appleLaunchAuthorizationMAC(key, claim, wire[:40]))
	if !validAppleLaunchAuthorization(key, claim, wire[:], issued.Add(appleLaunchAuthorizationTTL-time.Nanosecond)) {
		t.Fatal("authorization rejected before expiry")
	}
	sibling, replacement, next := claim, claim, claim
	sibling.Family = "sibling"
	replacement.AppInstance[0]++
	next.Attempt.ID = "new checkout"
	for _, tc := range []struct {
		name  string
		key   [32]byte
		claim billing.NativeCancellation
		now   time.Time
	}{
		{"expired", key, claim, issued.Add(appleLaunchAuthorizationTTL)},
		{"future issue time", key, claim, issued.Add(-time.Second)},
		{"another session", key, sibling, issued},
		{"replaced installation", key, replacement, issued},
		{"another checkout", key, next, issued},
		{"server restart", [32]byte{2}, claim, issued},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if validAppleLaunchAuthorization(tc.key, tc.claim, wire[:], tc.now) {
				t.Fatal("invalid authorization accepted")
			}
		})
	}
}

func (f *appleAttestationFixture) BeginEnrollment(_ context.Context, owner, family string, _ time.Time) ([32]byte, error) {
	f.owner, f.family = owner, family
	f.challenge = [32]byte{12}
	return f.challenge, nil
}

func (f *appleAttestationFixture) Enroll(_ context.Context, owner, family string, challenge, key [32]byte, proof []byte, fence func(func() error) error, _ func() time.Time) error {
	if owner != f.owner || family != f.family || f.challenge == ([32]byte{}) || challenge != f.challenge || string(proof) != "verified fixture" {
		return billing.ErrPurchaseBinding
	}
	return fence(func() error {
		f.key = key
		f.challenge = [32]byte{}
		return nil
	})
}

func (f *appleAttestationFixture) EnrolledInstance(_ context.Context, owner, family string) ([32]byte, error) {
	if owner != f.owner || family != f.family || f.key == ([32]byte{}) {
		return [32]byte{}, billing.ErrPurchaseBinding
	}
	return f.key, nil
}

func (f *appleAttestationFixture) VerifyLaunch(_ context.Context, c billing.NativeCancellation, proof []byte) error {
	if c.Owner != f.owner || c.Family != f.family || c.AppInstance != f.key || c.Challenge == ([32]byte{}) ||
		string(proof) != "verified exact launch" {
		return billing.ErrPurchaseBinding
	}
	return nil
}

func (f *appleAttestationFixture) VerifyCancellation(_ context.Context, c billing.NativeCancellation, proof []byte) error {
	if c.Owner != f.owner || c.Family != f.family || c.AppInstance != f.key || string(proof) != "verified cancellation" {
		return billing.ErrPurchaseBinding
	}
	if f.beforeCancel != nil {
		f.beforeCancel()
	}
	return nil
}

func TestNativeAppleCheckoutRequiresEnrollmentAndOriginalCancellationProof(t *testing.T) {
	a, _, _, _ := lifecycleHTTPFixture(t)
	const email = "native-http@example.test"
	signed := nativeFixtureLogin(t, a, a.Handler(nil))
	if err := a.Store.DB.Exec("UPDATE users SET email_verified_at=? WHERE email=?", time.Now().Unix(), email).Error; err != nil {
		t.Fatal(err)
	}
	journal, err := a.Config.Lifecycle.Billing(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	offer := billing.Offer{App: "com.example.preview", Product: "monthly", Interval: billing.Monthly, Store: billing.Apple, Environment: billing.Sandbox}
	b := &NativeBilling{Journal: journal, Environment: billing.Sandbox, CheckoutEnabled: true, SyntheticAccountsOnly: true,
		RequireAppleAttestation: true, Offers: []billing.Offer{offer}, Providers: map[billing.Store]billing.Provider{billing.Apple: billingVerifier(
			func(context.Context, string) (billing.ProviderProof, error) {
				return billing.ProviderProof{}, billing.ErrPurchaseBinding
			},
		)}}
	a.Config.NativeBilling = b
	h := a.Handler(nil)
	assert := func(want int, method, path, body, token string) string {
		t.Helper()
		w := callNative(h, method, path, body, token)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d (%s)", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	reply := func(body, field string) string {
		t.Helper()
		var v map[string]string
		if err := json.Unmarshal([]byte(body), &v); err != nil || v[field] == "" {
			t.Fatalf("missing %s in %s: %v", field, body, err)
		}
		return v[field]
	}
	payload := func(v map[string]string) string {
		body, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	enc := base64.RawURLEncoding.EncodeToString
	const begin = "/billing/apple/enrollment/challenge"
	const enroll = "/billing/apple/enrollment"
	const start = "/billing/checkouts"
	const status = "/billing/status"
	const plan = `{"interval":"monthly"}`
	token := signed.AccessToken
	assert(404, "POST", begin, "", token)
	assert(404, "GET", enroll, "", token)
	assert(404, "POST", start, plan, token)
	if got := assert(200, "GET", status, "", token); !strings.Contains(got, `"checkoutEnabled":false`) {
		t.Fatal("real checkout enabled without attestation")
	}
	f := &appleAttestationFixture{}
	b.AppleAttestation = &AppleBillingAttestation{Registry: f, Verifier: f, LaunchKey: [32]byte{71}}
	b.SyntheticAccountsOnly = false
	h = a.Handler(nil)
	assert(404, "POST", begin, "", token)
	assert(404, "POST", start, plan, token)
	b.SyntheticAccountsOnly = true
	h = a.Handler(nil)
	assert(401, "POST", begin, "", "")
	assert(404, "POST", start, plan, token)
	if got := assert(http.StatusOK, "GET", status, "", token); !strings.Contains(got, `"appleAttestationRequired":true`) {
		t.Fatal("iOS client cannot discover its enrollment requirement")
	}
	install := func(platform string) string {
		anon := nativeReply(t, callNative(h, "POST", "/session", `{"platform":"`+platform+`"}`, ""), 201)
		return nativeReply(t, callNative(h, "POST", "/login", `{"email":"`+email+`","password":"Garden native 2026!"}`, anon.AccessToken), 200).AccessToken
	}
	sibling, android := install("ios"), install("android")
	assert(404, "POST", begin, "", android)
	assert(404, "GET", enroll, "", android)
	assert(204, "GET", enroll, "", token)
	assert(400, "POST", begin, "{}", token)
	n := reply(assert(200, "POST", begin, "", token), "challenge")
	key := [32]byte{42}
	input := payload(map[string]string{"challenge": n, "keyID": enc(key[:]), "attestation": enc([]byte("verified fixture"))})
	assert(404, "POST", enroll, input, sibling)
	assert(204, "POST", enroll, input, token)
	if got := reply(assert(200, "GET", enroll, "", token), "keyID"); got != enc(key[:]) {
		t.Fatal("readiness did not return the enrolled installation's key")
	}
	assert(204, "GET", enroll, "", sibling)
	assert(404, "POST", enroll, input, token)
	assert(404, "POST", start, plan, sibling)
	id := reply(assert(201, "POST", start, plan, token), "id")
	path := start + "/" + id
	pathCancel := path + "/cancel"
	assert(404, "POST", path+"/launch", "", sibling)
	assert(404, "POST", path+"/launch-challenge", "", sibling)
	assert(404, "POST", path+"/launch-challenge", "", android)
	assert(400, "POST", path+"/launch", "", token)
	requestLaunch := func(path string) string {
		return reply(assert(200, "POST", path+"/launch-challenge", "", token), "authorization")
	}
	launchBody := func(authorization, proof string) string {
		return payload(map[string]string{"authorization": authorization, "assertion": enc([]byte(proof))})
	}
	authorization := requestLaunch(path)
	assert(404, "POST", path+"/launch", launchBody(authorization, "wrong build"), token)
	mutated, err := base64.RawURLEncoding.DecodeString(authorization)
	if err != nil || len(mutated) != 72 {
		t.Fatal("launch authorization format is invalid", err)
	}
	mutated[len(mutated)-1] ^= 1
	assert(404, "POST", path+"/launch", launchBody(enc(mutated), "verified exact launch"), token)
	body := launchBody(authorization, "verified exact launch")
	issued := assert(200, "POST", path+"/launch", body, token)
	assert(409, "POST", path+"/launch", body, token)
	launch := reply(issued, "challenge")
	if reply(issued, "interval") != "monthly" || reply(issued, "product") != "monthly" || reply(issued, "checkout") != id {
		t.Fatal("the native app did not receive the original purchase parameters")
	}
	assert(409, "DELETE", path, "", token)
	assert(400, "POST", pathCancel, `{"terminal":true}`, token)
	assert(404, "POST", pathCancel, payload(map[string]string{"challenge": launch, "assertion": enc([]byte("untrusted result"))}), token)
	proof := payload(map[string]string{"challenge": launch, "assertion": enc([]byte("verified cancellation"))})
	assert(404, "POST", pathCancel, proof, sibling)
	assert(404, "POST", pathCancel, proof, android)
	assert(409, "POST", start, plan, token)
	assert(204, "POST", pathCancel, proof, token)
	assert(404, "POST", pathCancel, proof, token)
	next := reply(assert(201, "POST", start, plan, token), "id")
	if next == id {
		t.Fatal("accepted cancellation did not release the original checkout")
	}
	nextPath := start + "/" + next
	assert(404, "POST", nextPath+"/launch", body, token)
	newAuthorization := requestLaunch(nextPath)
	newChallenge := reply(assert(200, "POST", nextPath+"/launch", launchBody(newAuthorization, "verified exact launch"), token), "challenge")
	f.beforeCancel = func() {
		assert(204, "POST", "/logout", "{}", token)
	}
	assert(401, "POST", nextPath+"/cancel",
		payload(map[string]string{"challenge": newChallenge, "assertion": enc([]byte("verified cancellation"))}), token)
	if got := assert(200, "GET", status, "", sibling); !strings.Contains(got, `"pending":true`) {
		t.Fatal("a sign-out during proof verification released an unresolved checkout")
	}
}
