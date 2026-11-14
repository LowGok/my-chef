package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/newsletter"
	"github.com/LowGok/my-chef/internal/store"
)

func TestNewsletterIdentityHTTPWebAndNative(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "web"
		if native {
			name = "native"
		}
		t.Run(name, func(t *testing.T) {
			a, _ := testAPI(t)
			keys, err := mailqueue.NewProofKeys("test", map[string][]byte{"test": bytes.Repeat([]byte{3}, 32)})
			if err != nil {
				t.Fatal(err)
			}
			j, err := a.Store.InitializeLifecycleJournal(t.Context(), a.Store.Path+".lifecycle")
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			a.Config.EmailRateKey = bytes.Repeat([]byte{4}, 32)
			n := &newsletter.Service{Store: a.Store, Keys: keys, RateKey: a.Config.EmailRateKey, Origin: "https://mychef.dev", ConsentVersion: "newsletter-v1", Lifecycle: j}
			signup := &auth.SignUp{Store: a.Store, Keys: keys, RateKey: n.RateKey, Origin: a.Config.Origin, Lifecycle: j, Newsletter: n}
			a.Config.SignUp, a.Config.EmailChange, a.Config.Lifecycle = signup, &auth.EmailChange{SignUp: signup}, j
			capture := func(address string) store.User {
				t.Helper()
				if _, err := n.Request(t.Context(), "192.0.2.60", newsletter.Input{Email: address, Locale: "en", ConsentVersion: n.ConsentVersion}); err != nil {
					t.Fatal(err)
				}
				if worked, err := n.Step(t.Context()); !worked || err != nil {
					t.Fatal(err)
				}
				var user store.User
				if err := a.Store.DB.Where("email_key=?", address).Take(&user).Error; err != nil {
					t.Fatal(err)
				}
				return user
			}
			contact := capture("reader@example.test")
			h := a.Handler(nil)
			var token string
			if native {
				token = nativeReply(t, callNative(h, "POST", "/session", `{"platform":"ios"}`, ""), 201).AccessToken
			} else {
				token = sessionCookie(t, call(h, "GET", "/api/v1/session", "", "", "", ""))
			}
			post := func(path, body string) *httptest.ResponseRecorder {
				if native {
					return callNative(h, "POST", path, body, token)
				}
				return call(h, "POST", "/api/v1"+path, body, token, auth.CSRF(token), a.Config.Origin)
			}
			encode := func(body any) string {
				b, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			request := func(email string) *httptest.ResponseRecorder {
				return post("/register", encode(map[string]string{"email": email, "firstName": "Reader", "lastName": "Fixture", "username": "reader"}))
			}
			old, fresh := request(contact.Email), request("unknown@example.test")
			if old.Code != 202 || fresh.Code != 202 || old.Body.String() != fresh.Body.String() || len(old.Result().Cookies()) != 0 {
				t.Fatal("signup acknowledgement disclosed contact classification")
			}
			for range 2 {
				if _, err := signup.Step(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			proof := func(template, recipient, field string) string {
				t.Helper()
				var row mailqueue.Record
				if err := a.Store.DB.Where("template=? AND recipient=?", template, recipient).Order("id DESC").Take(&row).Error; err != nil {
					t.Fatal(err)
				}
				fields, _, err := mailqueue.OpenProtected(keys, row, time.Now(), false)
				if err != nil {
					t.Fatal(err)
				}
				u, err := url.Parse(fields[field])
				if err != nil || u.Fragment == "" {
					t.Fatal("proof absent", err)
				}
				return u.Fragment
			}
			signupProof := proof(mailqueue.SignUpProofTemplate, contact.Email, "link")
			if w := post("/signup/inspect", encode(map[string]string{"proof": signupProof})); w.Code != 200 {
				t.Fatal("inspect", w.Code)
			}
			password := "HTTP newsletter fixture password"
			if w := post("/signup/complete", encode(map[string]string{"proof": signupProof, "firstName": "Chosen", "lastName": "Reader", "username": "chosen", "password": password})); w.Code != 201 || len(w.Result().Cookies()) != 0 {
				t.Fatal("completion", w.Code)
			}
			var user store.User
			if err := a.Store.DB.First(&user, contact.ID).Error; err != nil || user.IsNewsletterOnly {
				t.Fatal("HTTP promotion changed identity", err)
			}
			login := post("/login", encode(map[string]string{"email": contact.Email, "password": password}))
			if native {
				token = nativeReply(t, login, 200).AccessToken
			} else {
				if login.Code != 200 {
					t.Fatal("login", login.Code)
				}
				token = sessionCookie(t, login)
			}
			destination := capture("destination@example.test")
			if w := post("/email/change", encode(map[string]string{"email": destination.Email, "currentPassword": password})); w.Code != 202 {
				t.Fatal("email change start", w.Code)
			}
			first := proof(mailqueue.EmailChangeCurrentTemplate, contact.Email, "approve")
			second := proof(mailqueue.EmailChangeProposedTemplate, destination.Email, "link")
			if w := post("/email/change/decision", encode(map[string]string{"proof": first})); w.Code != 200 || !strings.Contains(w.Body.String(), "pending") {
				t.Fatal("first proof", w.Code)
			}
			if w := post("/email/change/decision", encode(map[string]string{"proof": second})); w.Code != 200 || !strings.Contains(w.Body.String(), "changed") {
				t.Fatal("final proof", w.Code)
			}
			if err := a.Store.DB.First(&user, contact.ID).Error; err != nil || user.EmailKey != destination.EmailKey {
				t.Fatal("email change lost app userID", err)
			}
		})
	}
}
