package httpapi

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LowGok/my-chef/internal/auth"
)

func TestSignInIdentifierEmailAndUsernameShareLastSlotAcrossTransports(t *testing.T) {
	a, h := testAPI(t)
	issue41Register(t, a, "exact-fixture@example.test", "Étage")
	web := issue41Anonymous(t, a)
	nat := nativeReply(t, callNative(h, "POST", "/session", `{"platform":"android"}`, ""), 201)
	for i := 0; i < 9; i++ {
		w := issue41Login(h, a.Config.Origin, web, "\tEXACT-fixture@Example.Test ", "wrong fixture password", fmt.Sprintf("192.0.2.%d:1100", i+10), false)
		issue41Error(t, w, 401, "unauthenticated", "The email or password is incorrect.")
	}
	start := make(chan struct{})
	result := make(chan *httptest.ResponseRecorder, 2)
	go func() {
		<-start
		result <- issue41Login(h, a.Config.Origin, web, "exact-fixture@example.test", "wrong fixture password", "192.0.2.31:1100", false)
	}()
	go func() {
		<-start
		result <- issue41Login(h, a.Config.Origin, nat.AccessToken, "\u2003ÉTAGE ", "wrong fixture password", "192.0.2.32:1100", true)
	}()
	close(start)
	outcome := map[int]int{}
	for range 2 {
		w := <-result
		outcome[w.Code]++
		if w.Code == 429 {
			issue41Error(t, w, 429, "rate_limited", "Too many attempts. Wait a minute and try again.")
			if w.Header().Get("Retry-After") != "60" {
				t.Fatal("wrong Retry-After")
			}
		}
	}
	if outcome[401] != 1 || outcome[429] != 1 {
		t.Fatalf("expected exactly one remaining shared email and username slot, got %v", outcome)
	}
}

func TestSignInIdentifierUsernameRotatesTheSameWebAndNativeAccount(t *testing.T) {
	a, h := testAPI(t)
	u := issue41Register(t, a, "alias-owner@example.test", "Étage Straße")
	old := issue41Anonymous(t, a)
	const alias = "\u2003ÉTAGE STRASSE\t"
	w := issue41Login(h, a.Config.Origin, old, alias, issue41FixturePassword, "192.0.2.1:1010", false)
	if w.Code != 200 {
		t.Fatalf("web username sign-in: %d", w.Code)
	}
	browser := sessionCookie(t, w)
	if s, err := a.Auth.Load(t.Context(), browser); browser == old || err != nil || s.UserID == nil || *s.UserID != u.ID {
		t.Fatal("web sign-in did not rotate to the username account")
	}
	if _, err := a.Auth.Load(t.Context(), old); !errors.Is(err, auth.ErrSession) {
		t.Fatal("old browser session stayed live")
	}
	for _, platform := range []string{"ios", "android"} {
		start := nativeReply(t, callNative(h, "POST", "/session", fmt.Sprintf(`{"platform":%q}`, platform), ""), 201)
		signed := nativeReply(t, issue41Login(h, a.Config.Origin, start.AccessToken, alias, issue41FixturePassword, "192.0.2.2:1010", true), 200)
		current, err := a.Auth.NativeLoad(t.Context(), signed.AccessToken)
		if err != nil || current.Session.UserID == nil || *current.Session.UserID != u.ID || current.Platform != platform {
			t.Fatal("native alias reached a different identity")
		}
		if _, err := a.Auth.NativeLoad(t.Context(), start.AccessToken); !errors.Is(err, auth.ErrSession) {
			t.Fatal("old native access stayed live")
		}
	}
}

func TestSignInIdentifierUnresolvedVariantsShareLimitWithoutChargingKnownAccount(t *testing.T) {
	a, h := testAPI(t)
	issue41Register(t, a, "owner@example.test", "Etage")
	web := issue41Anonymous(t, a)
	native := nativeReply(t, callNative(h, "POST", "/session", `{"platform":"ios"}`, ""), 201)
	variants := []string{"Étage", "\t ÉTAGE ", "éTaGe"}
	for i := 0; i < 10; i++ {
		w := issue41Login(h, a.Config.Origin, web, variants[i%len(variants)], "wrong fixture password", fmt.Sprintf("192.0.2.%d:1100", i+10), false)
		issue41Error(t, w, 401, "unauthenticated", "The email or password is incorrect.")
	}
	w := issue41Login(h, a.Config.Origin, native.AccessToken, "\u2003éTAGE", "wrong fixture password", "192.0.2.31:1100", true)
	issue41Error(t, w, 429, "rate_limited", "Too many attempts. Wait a minute and try again.")
	w = issue41Login(h, a.Config.Origin, web, " étage@example.test ", "wrong fixture password", "192.0.2.32:1100", false)
	issue41Error(t, w, 401, "unauthenticated", "The email or password is incorrect.")
	w = issue41Login(h, a.Config.Origin, web, "ETAGE", issue41FixturePassword, "192.0.2.33:1100", false)
	if w.Code != 200 {
		t.Fatalf("unresolved input limited a different account: %d", w.Code)
	}
}

func TestSignInIdentifierUsernamePreservesVerificationRestriction(t *testing.T) {
	a, _, _ := recoveryAPI(t)
	a.Config.Verification = &auth.EmailVerification{SignUp: a.Config.Recovery.SignUp}
	h := a.Handler(nil)
	issue41Register(t, a, "restricted@example.test", "Étage")
	for _, isNative := range []bool{false, true} {
		token := issue41Anonymous(t, a)
		if isNative {
			token = nativeReply(t, callNative(h, "POST", "/session", `{"platform":"ios"}`, ""), 201).AccessToken
		}
		w := issue41Login(h, a.Config.Origin, token, " éTaGe ", issue41FixturePassword, "203.0.113.4:9000", isNative)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"verificationRequired":true`) || !strings.Contains(w.Body.String(), `"user":null`) {
			t.Fatalf("alias bypassed restricted response on native=%t", isNative)
		}
		if isNative {
			token = nativeReply(t, w, 200).AccessToken
			w = callNative(h, "GET", "/profile", "", token)
		} else {
			w = call(h, "GET", "/api/v1/profile", "", sessionCookie(t, w), "", "")
		}
		if w.Code != 403 || !strings.Contains(w.Body.String(), "email_verification_required") {
			t.Fatalf("alias bypassed profile restriction on native=%t", isNative)
		}
	}
}
