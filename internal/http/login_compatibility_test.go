package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/store"
)

const issue41FixturePassword = "Synthetic garden account password 2026!"

func issue41Register(t *testing.T, a *API, email, username string) store.User {
	t.Helper()
	u, err := a.Auth.Register(t.Context(), auth.Registration{
		Email: email, Password: issue41FixturePassword, Username: username,
		FirstName: "Synthetic", LastName: "Account",
	})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func issue41Anonymous(t *testing.T, a *API) string {
	t.Helper()
	token, err := a.Auth.Rotate(t.Context(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func issue41Login(h http.Handler, origin, token, identifier, password, source string, native bool) *httptest.ResponseRecorder {
	data, _ := json.Marshal(map[string]string{"email": identifier, "password": password})
	path := "/api/v1/login"
	if native {
		path = "/api/native/v1/login"
	}
	r := httptest.NewRequest("POST", path, strings.NewReader(string(data)))
	r.RemoteAddr = source
	r.Header.Set("Content-Type", "application/json")
	if native {
		r.Header.Set("Authorization", "Bearer "+token)
	} else {
		r.AddCookie(&http.Cookie{Name: "mychef_session", Value: token})
		r.Header.Set("X-CSRF-Token", auth.CSRF(token))
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func issue41Error(t *testing.T, w *httptest.ResponseRecorder, status int, code, message string) {
	t.Helper()
	var body struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"requestId"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal("error was not JSON")
	}
	if w.Code != status || body.Error.Code != code || body.Error.Message != message ||
		body.Error.RequestID == "" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("X-MyChef-User") != "" {
		t.Fatalf("unexpected error shape, authority or status: %d %s", w.Code, body.Error.Code)
	}
}

func TestSignInCompatibilityEmailSignInAcrossTransports(t *testing.T) {
	a, h := testAPI(t)
	u := issue41Register(t, a, "Mixed.Mailbox@Example.Test", "fixture-owner")
	old := issue41Anonymous(t, a)
	w := issue41Login(h, a.Config.Origin, old, "\u2003 MIXED.mailbox@example.test\t", issue41FixturePassword, "192.0.2.1:1010", false)
	if w.Code != 200 {
		t.Fatalf("web email sign-in: %d", w.Code)
	}
	browser := sessionCookie(t, w)
	if s, err := a.Auth.Load(t.Context(), browser); browser == old || err != nil || s.UserID == nil || *s.UserID != u.ID {
		t.Fatal("web sign-in did not rotate to the correct account")
	}
	if _, err := a.Auth.Load(t.Context(), old); !errors.Is(err, auth.ErrSession) {
		t.Fatal("web login left the anonymous cookie live")
	}

	for _, platform := range []string{"ios", "android"} {
		start := nativeReply(t, callNative(h, "POST", "/session", fmt.Sprintf(`{"platform":%q}`, platform), ""), 201)
		w = issue41Login(h, a.Config.Origin, start.AccessToken, " mixed.MAILBOX@EXAMPLE.TEST ", issue41FixturePassword, "192.0.2.2:1010", true)
		signed := nativeReply(t, w, 200)
		got, err := a.Auth.NativeLoad(t.Context(), signed.AccessToken)
		if err != nil || got.Platform != platform || got.Session.UserID == nil || *got.Session.UserID != u.ID {
			t.Fatal("native sign-in reached a different account or transport")
		}
		if _, err = a.Auth.NativeLoad(t.Context(), start.AccessToken); !errors.Is(err, auth.ErrSession) {
			t.Fatal("native login left the anonymous access live")
		}
	}
}

func TestSignInCompatibilityGenericCredentialsAndNoSessionMutation(t *testing.T) {
	a, h := testAPI(t)
	issue41Register(t, a, "known@example.test", "fixture-known")
	browser := issue41Anonymous(t, a)
	native := nativeReply(t, callNative(h, "POST", "/session", `{"platform":"ios"}`, ""), 201)
	for _, request := range []struct{ identifier, password string }{
		{"known@example.test", "wrong fixture password"},
		{"missing@example.test", issue41FixturePassword},
		{"malformed@", issue41FixturePassword},
		{"known@example.test", ""},
	} {
		for _, isNative := range []bool{false, true} {
			token := browser
			if isNative {
				token = native.AccessToken
			}
			w := issue41Login(h, a.Config.Origin, token, request.identifier, request.password, "192.0.2.3:1000", isNative)
			issue41Error(t, w, 401, "unauthenticated", "The email or password is incorrect.")
		}
	}
	if s, err := a.Auth.Load(t.Context(), browser); err != nil || s.UserID != nil {
		t.Fatal("credential failure changed browser authority")
	}
	if s, err := a.Auth.NativeLoad(t.Context(), native.AccessToken); err != nil || s.Session.UserID != nil {
		t.Fatal("credential failure changed native authority")
	}
}

func TestSignInCompatibilitySharedCanonicalEmailAndSourceLimits(t *testing.T) {
	a, h := testAPI(t)
	issue41Register(t, a, "fixture-limit@example.test", "fixture-limit")
	issue41Register(t, a, "fixture-separate@example.test", "fixture-separate")
	browser := issue41Anonymous(t, a)
	native := nativeReply(t, callNative(h, "POST", "/session", `{"platform":"android"}`, ""), 201)
	emailForms := []string{"fixture-limit@example.test", "\t FIXTURE-LIMIT@EXAMPLE.TEST ", "fixture-LIMIT@Example.Test"}
	send := func(index int, isNative bool, source string) *httptest.ResponseRecorder {
		token := browser
		if isNative {
			token = native.AccessToken
		}
		return issue41Login(h, a.Config.Origin, token, emailForms[index%len(emailForms)], "wrong fixture password", source, isNative)
	}
	for i := 0; i < 9; i++ {
		w := send(i, i%2 == 0, fmt.Sprintf("192.0.2.%d:7000", i+10))
		issue41Error(t, w, 401, "unauthenticated", "The email or password is incorrect.")
	}
	const limitMessage = "Too many attempts. Wait a minute and try again."
	result := make(chan *httptest.ResponseRecorder, 2)
	start := make(chan struct{})
	go func() { <-start; result <- send(9, false, "192.0.2.30:7100") }()
	go func() { <-start; result <- send(10, true, "192.0.2.31:7101") }()
	close(start)
	statusCount := map[int]int{}
	for i := 0; i < 2; i++ {
		w := <-result
		statusCount[w.Code]++
		if w.Code == 429 {
			issue41Error(t, w, 429, "rate_limited", limitMessage)
			if w.Header().Get("Retry-After") != "60" {
				t.Fatal("wrong Retry-After")
			}
		}
	}
	if statusCount[401] != 1 || statusCount[429] != 1 {
		t.Fatalf("expected one remaining canonical-email slot across both transports, got %v", statusCount)
	}
	w := issue41Login(h, a.Config.Origin, browser, "fixture-separate@example.test", "wrong fixture password", "192.0.2.32:8000", false)
	issue41Error(t, w, 401, "unauthenticated", "The email or password is incorrect.")
	for i := 0; i < 16; i++ {
		isNative := i%2 == 0
		token := browser
		if isNative {
			token = native.AccessToken
		}
		w = issue41Login(h, a.Config.Origin, token, fmt.Sprintf("invalid-entry-%d", i), "wrong fixture password", fmt.Sprintf("198.51.100.1:%d", 9000+i), isNative)
		if i < 15 {
			issue41Error(t, w, 401, "unauthenticated", "The email or password is incorrect.")
		} else {
			issue41Error(t, w, 429, "rate_limited", limitMessage)
			if w.Header().Get("Retry-After") != "60" {
				t.Fatal("wrong source Retry-After")
			}
		}
	}
	w = issue41Login(h, a.Config.Origin, browser, "another-invalid-entry", "wrong fixture password", "198.51.100.2:9999", false)
	issue41Error(t, w, 401, "unauthenticated", "The email or password is incorrect.")
}

func TestSignInCompatibilityVerificationAndEmailOnlyRecovery(t *testing.T) {
	a, _, anonymous := recoveryAPI(t)
	a.Config.Verification = &auth.EmailVerification{SignUp: a.Config.Recovery.SignUp}
	h := a.Handler(nil)
	u := issue41Register(t, a, "Restricted@Example.Test", "fixture-restricted")
	browser := issue41Anonymous(t, a)
	w := issue41Login(h, a.Config.Origin, browser, "restricted@example.test", issue41FixturePassword, "203.0.113.1:9000", false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"verificationRequired":true`) ||
		!strings.Contains(w.Body.String(), `"user":null`) {
		t.Fatal("browser exposed profile before verification")
	}
	restrictedBrowser := sessionCookie(t, w)
	w = call(h, "GET", "/api/v1/profile", "", restrictedBrowser, "", "")
	if w.Code != 403 || !strings.Contains(w.Body.String(), "email_verification_required") {
		t.Fatal("browser profile allowed before verification")
	}
	native := nativeReply(t, callNative(h, "POST", "/session", `{"platform":"ios"}`, ""), 201)
	w = issue41Login(h, a.Config.Origin, native.AccessToken, " restricted@EXAMPLE.TEST ", issue41FixturePassword, "203.0.113.2:9000", true)
	restrictedNative := nativeReply(t, w, 200)
	if !strings.Contains(w.Body.String(), `"verificationRequired":true`) || !strings.Contains(w.Body.String(), `"user":null`) {
		t.Fatal("native login exposed profile before verification")
	}
	w = callNative(h, "GET", "/profile", "", restrictedNative.AccessToken)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "email_verification_required") {
		t.Fatal("native profile allowed before verification")
	}
	w = call(h, "POST", requestRecoveryPath, `{"email":"fixture-restricted"}`, anonymous, auth.CSRF(anonymous), a.Config.Origin)
	if w.Code != 400 || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("recovery accepted a plain username, status %d", w.Code)
	}
	var jobs int64
	if err := a.Store.DB.Model(&store.RecoveryJob{}).Count(&jobs).Error; err != nil || jobs != 0 {
		t.Fatal("recovery enqueued work for a plain username")
	}
	if err := a.Store.DB.Model(&u).Update("email_verified_at", time.Now().Unix()).Error; err != nil {
		t.Fatal(err)
	}
	browser = issue41Anonymous(t, a)
	w = issue41Login(h, a.Config.Origin, browser, "restricted@example.test", issue41FixturePassword, "203.0.113.3:9000", false)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"verificationRequired"`) || !strings.Contains(w.Body.String(), `"username":"fixture-restricted"`) ||
		call(h, "GET", "/api/v1/profile", "", sessionCookie(t, w), "", "").Code != 200 {
		t.Fatal("verified email could not use the same account profile")
	}
}

func TestSignInCompatibilityEmailShapeAlwaysUsesMailbox(t *testing.T) {
	a, h := testAPI(t)
	owner := issue41Register(t, a, "Mailbox.Owner@Example.Test", "fixture-mailbox-owner")
	const otherPassword = "Different synthetic kitchen password!"
	other, err := a.Auth.Register(t.Context(), auth.Registration{
		Email: "other-owner@example.test", Username: "mailbox.owner@example.test", Password: otherPassword,
		FirstName: "Other", LastName: "Account",
	})
	if err != nil || other.ID == owner.ID {
		t.Fatal("unable to construct separate synthetic alias owner")
	}
	orphan, err := a.Auth.Register(t.Context(), auth.Registration{
		Email: "actual-orphan-owner@example.test", Username: "unused-address@example.test", Password: otherPassword,
		FirstName: "Another", LastName: "Account",
	})
	if err != nil || orphan.ID == owner.ID {
		t.Fatal("unable to construct synthetic email-shaped alias without a mailbox")
	}
	token := issue41Anonymous(t, a)
	for _, input := range []string{"\tMAILBOX.OWNER@EXAMPLE.TEST ", "unused-address@example.test"} {
		w := issue41Login(h, a.Config.Origin, token, input, otherPassword, "192.0.2.40:8000", false)
		issue41Error(t, w, 401, "unauthenticated", "The email or password is incorrect.")
	}
	for _, account := range []struct {
		email, password string
		id              int64
	}{{owner.Email, issue41FixturePassword, owner.ID}, {other.Email, otherPassword, other.ID}} {
		w := issue41Login(h, a.Config.Origin, issue41Anonymous(t, a), account.email, account.password, "192.0.2.41:8000", false)
		if w.Code != 200 {
			t.Fatalf("mailbox login returned %d", w.Code)
		}
		s, err := a.Auth.Load(t.Context(), sessionCookie(t, w))
		if err != nil || s.UserID == nil || *s.UserID != account.id {
			t.Fatal("mailbox login selected a different account")
		}
	}
}
