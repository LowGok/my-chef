package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/domain"
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

func TestNativeIdentityRoutingHeadersDoNotReachHandlers(t *testing.T) {
	a, _ := testAPI(t)
	h := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"X-MyChef-Identity", "X-MyChef-Identity-Lane", "X-MyChef-Native-Lane", "X-MyChef-App-ID"} {
			if r.Header.Get(name) != "" {
				t.Error("client routing metadata reached handler")
			}
		}
		w.WriteHeader(204)
	}))
	r := httptest.NewRequest("GET", "/api/native/v1/fixture-operation", nil)
	for _, name := range []string{"X-MyChef-Identity", "X-MyChef-Identity-Lane", "X-MyChef-Native-Lane", "X-MyChef-App-ID"} {
		r.Header.Set(name, "app")
	}
	h.ServeHTTP(httptest.NewRecorder(), r)
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
	if callAppNative(restarted, "GET", "/profile", "", refreshed.AccessToken).Code != 403 {
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

func TestNativeIdentityAccountContinuityAcrossWithdrawal(t *testing.T) {
	a, legacy := testAPI(t)
	preview := nativeFixtureLogin(t, a, legacy)
	a.Config.NativeAppIngress = true
	active, err := a.Store.SetNativeIdentityPolicy(t.Context(), 0, true, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	shared := a.Handler(nil)
	anonymous := nativeReply(t, callAppNative(shared, "POST", "/session", `{"platform":"android"}`, ""), 201)
	app := nativeReply(t, callAppNative(shared, "POST", "/login", `{"email":"native-http@example.test","password":"Garden native 2026!"}`, anonymous.AccessToken), 200)

	oldProfile := callNative(shared, "GET", "/profile", "", preview.AccessToken)
	newProfile := callAppNative(shared, "GET", "/profile", "", app.AccessToken)
	account := oldProfile.Header().Get("X-MyChef-User")
	if oldProfile.Code != 200 || newProfile.Code != 200 || account == "" || account != newProfile.Header().Get("X-MyChef-User") || preview.AccessToken == app.AccessToken {
		t.Fatal("two installations did not receive separate sessions for the same account")
	}

	const week = "/shopping/2026-11-16"
	shopping := func(w *httptest.ResponseRecorder, expected int) domain.ShoppingList {
		t.Helper()
		if w.Code != expected {
			t.Fatalf("shopping returned %d, want %d", w.Code, expected)
		}
		var list domain.ShoppingList
		if expected == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
		}
		return list
	}
	payload := func(list domain.ShoppingList, id, name string) string {
		t.Helper()
		list.Items = append(list.Items, domain.ShoppingItem{
			ID: id, Name: name, State: "buy", Manual: true, Contributions: []domain.ShoppingContribution{},
		})
		body, err := json.Marshal(list)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}

	initial := shopping(callAppNative(shared, "GET", week, "", app.AccessToken), 200)
	staleApp := payload(initial, "candidate-rice", "Candidate rice")
	previewEdit := payload(initial, "preview-beans", "Preview beans")
	saved := shopping(callNative(shared, "PUT", week, previewEdit, preview.AccessToken), 200)
	shopping(callAppNative(shared, "PUT", week, staleApp, app.AccessToken), 409)

	paused, err := a.Store.SetNativeIdentityPolicy(t.Context(), active.Revision, false, time.Now().Add(time.Minute).Unix(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(a.Store, nil, a.Config, nil).Handler(nil)
	if callAppNative(restarted, "GET", "/profile", "", app.AccessToken).Code != 503 ||
		callNative(restarted, "GET", week, "", app.AccessToken).Code != 401 {
		t.Fatal("paused credential reached a privileged route")
	}
	current := shopping(callAppNative(restarted, "GET", week, "", app.AccessToken), 200)
	if len(current.Items) != 1 || current.Items[0].Name != "Preview beans" || current.Revision != saved.Revision {
		t.Fatal("new installation did not see the shared shopping revision")
	}
	checked := payload(current, "candidate-rice", "Candidate rice")
	shopping(callAppNative(restarted, "PUT", week, checked, app.AccessToken), 200)
	if _, err := a.Store.SQL.Exec("UPDATE native_identity_policy SET sync_until=unixepoch()-1 WHERE revision=?", paused.Revision); err != nil {
		t.Fatal(err)
	}
	shopping(callAppNative(restarted, "PUT", week, checked, app.AccessToken), 503)
	final := shopping(callNative(restarted, "GET", week, "", preview.AccessToken), 200)
	if len(final.Items) != 2 {
		t.Fatal("withdrawal lost synced edits or duplicated a shopping item")
	}
	names := map[string]bool{}
	for _, item := range final.Items {
		names[item.Name] = true
	}
	if !names["Preview beans"] || !names["Candidate rice"] {
		t.Fatal("Preview did not retain both installations' synced changes")
	}
}
