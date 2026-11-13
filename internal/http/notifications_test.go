package httpapi

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/LowGok/my-chef/internal/store"
)

func TestReminderRegistrationUsesOnlyNativeServerIdentity(t *testing.T) {
	a, h := testAPI(t)
	tokens := nativeFixtureLogin(t, a, h)
	who, err := a.Auth.NativeLoad(t.Context(), tokens.AccessToken)
	if err != nil || who.Session.UserID == nil {
		t.Fatal("native user missing", err)
	}
	if err := a.Store.DB.Exec("UPDATE users SET email_verified_at=unixepoch() WHERE user_id=?", *who.Session.UserID).Error; err != nil {
		t.Fatal(err)
	}
	reminders, err := store.NewReminderSettings(a.Store, bytes.Repeat([]byte{7}, 32), store.EntitlementReader(store.FixtureEntitlementLease{}, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Config.Reminders = reminders
	h = a.Handler(nil)
	const prefs = `{"zone":"Europe/Brussels","current":true,"advance":false,"advanceDay":0,"advanceMinute":0}`
	const path = "/planning-reminders"
	if got := callNative(h, http.MethodPut, path, prefs, tokens.AccessToken).Code; got != 200 {
		t.Fatal("native preference update status", got)
	}
	token := strings.Repeat("a", 64)
	valid := `{"token":"` + token + `","osAllowed":true}`
	if got := callNative(h, http.MethodPut, path+"/installation", `{"token":"`+token+`","osAllowed":true,"platform":"android"}`, tokens.AccessToken).Code; got != 400 {
		t.Fatal("client-chosen platform accepted", got)
	}
	if got := callNative(h, http.MethodPut, path+"/installation", valid, tokens.AccessToken).Code; got != 204 {
		t.Fatal("verified iOS registration failed", got)
	}
	if got := callNative(h, http.MethodGet, path, "", tokens.AccessToken); got.Code != 200 || !strings.Contains(got.Body.String(), `"deviceEnabled":true`) || strings.Contains(got.Body.String(), token) {
		t.Fatal("installation status disclosed private token or was missing", got.Code)
	}
	if got := callNative(h, http.MethodDelete, path+"/installation", "", tokens.AccessToken).Code; got != 204 {
		t.Fatal("opt-out failed", got)
	}
}
