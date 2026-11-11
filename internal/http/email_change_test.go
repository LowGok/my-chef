package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
)

func TestEmailChangeHTTPLeavesUnrelatedSessionAlone(t *testing.T) {
	a, _ := testAPI(t)
	ctx := context.Background()
	keys, err := mailqueue.NewProofKeys("test", map[string][]byte{"test": bytes.Repeat([]byte{3}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	a.Config.EmailRateKey = bytes.Repeat([]byte{4}, 32)
	signup := &auth.SignUp{Store: a.Store, Keys: keys, RateKey: a.Config.EmailRateKey, Origin: a.Config.Origin}
	a.Config.EmailChange = &auth.EmailChange{SignUp: signup}
	const password = "fixture original account password"
	user := func(name string) string {
		t.Helper()
		u, err := a.Auth.Register(ctx, auth.Registration{Email: name + "@example.test", Password: password,
			Username: name, FirstName: name, LastName: "User"})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Store.DB.Model(&u).Update("email_verified_at", time.Now().Unix()).Error; err != nil {
			t.Fatal(err)
		}
		token, err := a.Auth.Rotate(ctx, &u.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	owner, other := user("owner"), user("other")
	h := a.Handler(nil)
	w := call(h, "POST", "/api/v1/email/change", `{"email":"replacement@example.test","currentPassword":"fixture original account password"}`,
		owner, auth.CSRF(owner), a.Config.Origin)
	if w.Code != http.StatusAccepted || len(w.Result().Cookies()) != 0 {
		t.Fatal("could not start address change")
	}
	proof := func(template, field string) string {
		t.Helper()
		var row mailqueue.Record
		if err := a.Store.DB.Where("template = ?", template).First(&row).Error; err != nil {
			t.Fatal(err)
		}
		fields, _, err := mailqueue.OpenProtected(keys, row, time.Now(), false)
		if err != nil {
			t.Fatal(err)
		}
		link, err := url.Parse(fields[field])
		if err != nil || link.Fragment == "" {
			t.Fatal("missing proof")
		}
		b, _ := json.Marshal(map[string]string{"proof": link.Fragment})
		return string(b)
	}
	approve := proof(mailqueue.EmailChangeCurrentTemplate, "approve")
	proposed := proof(mailqueue.EmailChangeProposedTemplate, "link")
	w = call(h, "POST", "/api/v1/email/change/inspect", approve, other, auth.CSRF(other), a.Config.Origin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "maskedAddress") || strings.Contains(w.Body.String(), "replacement@") {
		t.Fatal("current inbox received unmasked proposed address")
	}
	w = call(h, "POST", "/api/v1/email/change/decision", approve, other, auth.CSRF(other), a.Config.Origin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "pending") {
		t.Fatal("first inbox did not leave the change pending")
	}
	w = call(h, "POST", "/api/v1/email/change/decision", proposed, other, auth.CSRF(other), a.Config.Origin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"changed"`) ||
		strings.Contains(w.Body.String(), "signInRequired") || len(w.Result().Cookies()) != 0 {
		t.Fatal("finalization in another browser touched its session")
	}
	if _, err := a.Auth.Load(ctx, other); err != nil {
		t.Fatal("the other browser was signed out")
	}
}
