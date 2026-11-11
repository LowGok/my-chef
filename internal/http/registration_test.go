package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
)

func TestSignUpHTTPKeepsAcknowledgementUniformAndNeverSetsACookie(t *testing.T) {
	a, _ := testAPI(t)
	keys, err := mailqueue.NewProofKeys("test", map[string][]byte{"test": bytes.Repeat([]byte{3}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	a.Config.EmailRateKey = bytes.Repeat([]byte{4}, 32)
	a.Config.SignUp = &auth.SignUp{Store: a.Store, Keys: keys, RateKey: a.Config.EmailRateKey, Origin: a.Config.Origin}
	h := a.Handler(nil)
	ctx := context.Background()
	if _, err := a.Auth.Register(ctx, auth.Registration{Email: "Existing@Example.Test", Username: "existing",
		Password: "fixture existing password", FirstName: "Existing", LastName: "User"}); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, call(h, "GET", "/api/v1/session", "", "", "", ""))
	post := func(path, body string) *testRecorder {
		t.Helper()
		w := call(h, "POST", path, body, cookie, auth.CSRF(cookie), a.Config.Origin)
		if len(w.Result().Cookies()) != 0 {
			t.Fatal("sign-up changed the anonymous session cookie")
		}
		return &testRecorder{code: w.Code, body: w.Body.String()}
	}
	old := post("/api/v1/register", `{"email":"existing@example.test","firstName":"Untrusted","lastName":"User","username":"candidate"}`)
	fresh := post("/api/v1/register", `{"email":"New@Example.Test","firstName":"Untrusted","lastName":"User","username":"candidate"}`)
	if old.code != 202 || fresh.code != 202 || old.body != fresh.body {
		t.Fatalf("registration exposed an account-dependent acknowledgement: %d %d", old.code, fresh.code)
	}
	if got := post("/api/v1/register", `{"email":"other@example.test","firstName":"No","lastName":"Password","username":"candidate","password":"a chosen password"}`); got.code != 400 {
		t.Fatal("request route accepted a password")
	}
	for range 2 {
		if _, err := a.Config.SignUp.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var row mailqueue.Record
	if err := a.Store.DB.Where("template = ?", mailqueue.SignUpProofTemplate).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	fields, _, err := mailqueue.OpenProtected(keys, row, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(fields["link"])
	if err != nil || link.Fragment == "" {
		t.Fatal("worker did not produce a proof")
	}
	b, _ := json.Marshal(map[string]string{"proof": link.Fragment})
	if got := post("/api/v1/signup/inspect", string(b)); got.code != 200 || got.body != "{\"firstName\":\"Untrusted\",\"lastName\":\"User\",\"username\":\"candidate\"}\n" {
		t.Fatal("inspect returned more than the editable suggestions")
	}
	payload := map[string]string{"proof": link.Fragment, "firstName": "Owner", "lastName": "User", "username": "confirmed", "password": "only the mailbox owner chose this"}
	payload["email"] = "different@example.test"
	b, _ = json.Marshal(payload)
	if got := post("/api/v1/signup/complete", string(b)); got.code != http.StatusBadRequest {
		t.Fatal("the completion route accepted an override of the delivery address")
	}
	delete(payload, "email")
	b, _ = json.Marshal(payload)
	if got := post("/api/v1/signup/complete", string(b)); got.code != http.StatusCreated {
		t.Fatalf("completion did not create a verified account: %d", got.code)
	}
	if got := post("/api/v1/signup/complete", string(b)); got.code != http.StatusBadRequest {
		t.Fatal("consumed proof was not rejected")
	}
}

type testRecorder struct {
	code int
	body string
}
