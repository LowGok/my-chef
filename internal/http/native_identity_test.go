package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
)

func callAppNative(h http.Handler, method, path, body, access string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/native/app/v1"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if access != "" {
		r.Header.Set("Authorization", "Bearer "+access)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestNativeIdentityIngressAndPersistedWithdrawal(t *testing.T) {
	a, legacy := testAPI(t)
	old := nativeFixtureLogin(t, a, legacy)
	if callAppNative(legacy, "POST", "/session", `{"platform":"ios"}`, "").Code != 404 {
		t.Fatal("candidate route enabled without operator configuration")
	}
	a.Config.NativeAppIngress = true
	h := a.Handler(nil)
	if callAppNative(h, "POST", "/session", `{"platform":"ios"}`, "").Code != 503 {
		t.Fatal("candidate accepted new family without persistent authorization")
	}
	policy, err := a.Store.SetNativeIdentityPolicy(t.Context(), 0, true, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	anon := nativeReply(t, callAppNative(h, "POST", "/session", `{"platform":"ios"}`, ""), 201)
	unsigned := nativeReply(t, callAppNative(h, "POST", "/session", `{"platform":"ios"}`, ""), 201)
	signed := nativeReply(t, callAppNative(h, "POST", "/login", `{"email":"native-http@example.test","password":"Garden native 2026!"}`, anon.AccessToken), 200)
	if callAppNative(h, "GET", "/profile", "", signed.AccessToken).Code != 200 || callNative(h, "GET", "/profile", "", signed.AccessToken).Code != 401 || callAppNative(h, "GET", "/profile", "", old.AccessToken).Code != 401 {
		t.Fatal("server route did not isolate bearer authority")
	}
	spoof := httptest.NewRequest("GET", "/api/native/v1/profile", nil)
	spoof.Header.Set("Authorization", "Bearer "+signed.AccessToken)
	spoof.Header.Set("X-MyChef-Identity", "app")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, spoof)
	if w.Code != 401 {
		t.Fatal("header selected candidate authority")
	}
	deadline := time.Now().Add(time.Minute).Unix()
	paused, err := a.Store.SetNativeIdentityPolicy(t.Context(), policy.Revision, false, deadline, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/profile", "/session", "/shopping/2026-11-16/other", "/shopping/not-a-date"} {
		if callAppNative(h, "GET", path, "", signed.AccessToken).Code != 503 {
			t.Fatalf("paused candidate reached %s", path)
		}
	}
	if callAppNative(h, "POST", "/session", `{"platform":"ios"}`, "").Code != 503 {
		t.Fatal("paused candidate issued a new family")
	}
	refreshed := nativeReply(t, callAppNative(h, "POST", "/refresh", `{"refreshToken":"`+signed.RefreshToken+`"}`, ""), 200)
	if refreshed.AccessExpiresUnix > deadline || refreshed.SessionExpiresUnix > deadline {
		t.Fatal("sync credentials outlive approved deadline")
	}
	if w := callAppNative(h, "GET", "/shopping/2026-11-16", "", refreshed.AccessToken); w.Code != 200 {
		t.Fatalf("existing signed-in family cannot sync: %d", w.Code)
	}
	if callAppNative(h, "POST", "/refresh", `{"refreshToken":"`+unsigned.RefreshToken+`"}`, "").Code != 401 {
		t.Fatal("anonymous family acquired sync authority")
	}
	// Constructing a fresh API must not reset the persisted pause.
	restarted := New(a.Store, nil, a.Config, nil).Handler(nil)
	if callAppNative(restarted, "GET", "/profile", "", refreshed.AccessToken).Code != 503 || callNative(restarted, "GET", "/profile", "", old.AccessToken).Code != 200 {
		t.Fatal("restart reopened candidate or disabled Preview")
	}
	if _, err := a.Store.SetNativeIdentityPolicy(t.Context(), paused.Revision, true, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if callAppNative(restarted, "GET", "/profile", "", refreshed.AccessToken).Code != 401 {
		t.Fatal("re-enable revived pre-cutoff family")
	}
	if callNative(restarted, "GET", "/profile", "", old.AccessToken).Code != 200 {
		t.Fatal("candidate withdrawal damaged Preview")
	}
	if _, err := a.Auth.NativeLoad(auth.WithNativeIdentity(t.Context(), "app"), refreshed.AccessToken); err != nil {
		t.Fatal("policy refusal destroyed retained local sync authority", err)
	}
}

func TestNativeIdentityHTTPAdmissionSerializesWithdrawal(t *testing.T) {
	a, _ := testAPI(t)
	a.Config.NativeAppIngress = true
	if _, err := a.Store.SetNativeIdentityPolicy(t.Context(), 0, true, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	entered, finish := make(chan struct{}), make(chan struct{})
	h := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-finish; w.WriteHeader(204) }))
	requestDone := make(chan int, 1)
	go func() { requestDone <- callAppNative(h, "GET", "/fixture-operation", "", "").Code }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("fixture request never reached the wrapped handler")
	}
	paused := make(chan error, 1)
	go func() { _, err := a.Store.SetNativeIdentityPolicy(t.Context(), 1, false, 0, time.Now()); paused <- err }()
	select {
	case err := <-paused:
		close(finish)
		t.Fatalf("withdrawal finished before admitted request: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(finish)
	if code := <-requestDone; code != 204 {
		t.Fatal("admitted request failed")
	}
	if err := <-paused; err != nil {
		t.Fatal(err)
	}
	if callAppNative(h, "GET", "/fixture-operation", "", "").Code != 503 {
		t.Fatal("new request entered after acknowledged cutoff")
	}
}

func TestNativeIdentitySyncDeadlineClosesExistingCredentials(t *testing.T) {
	a, legacy := testAPI(t)
	old := nativeFixtureLogin(t, a, legacy)
	a.Config.NativeAppIngress = true
	if _, err := a.Store.SetNativeIdentityPolicy(t.Context(), 0, true, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	h := a.Handler(nil)
	anon := nativeReply(t, callAppNative(h, "POST", "/session", `{"platform":"android"}`, ""), 201)
	signed := nativeReply(t, callAppNative(h, "POST", "/login", `{"email":"native-http@example.test","password":"Garden native 2026!"}`, anon.AccessToken), 200)
	if _, err := a.Store.SetNativeIdentityPolicy(t.Context(), 1, false, time.Now().Add(time.Minute).Unix(), time.Now()); err != nil {
		t.Fatal(err)
	}
	// Expire only this disposable fixture's clocked window.
	if _, err := a.Store.SQL.Exec("UPDATE native_identity_policy SET sync_until=unixepoch()-1"); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "PUT"} {
		if callAppNative(h, method, "/shopping/2026-11-16", `{}`, signed.AccessToken).Code != 503 {
			t.Fatal("expired sync access accepted")
		}
	}
	if callAppNative(h, "POST", "/refresh", `{"refreshToken":"`+signed.RefreshToken+`"}`, "").Code != 503 {
		t.Fatal("refresh reopened an expired sync window")
	}
	if callNative(h, "GET", "/profile", "", old.AccessToken).Code != 200 {
		t.Fatal("expired candidate window stopped Preview")
	}
}
