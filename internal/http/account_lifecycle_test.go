package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/store"
)

const lifecyclePassword = "Garden table lifecycle 2026!"
const exportPath = "/api/v1/account/export"
const deletePath = "/api/v1/account/deletion"

func lifecycleHTTPFixture(t *testing.T) (*API, http.Handler, store.User, string) {
	t.Helper()
	a, _ := testAPI(t)
	// The executable always supplies the independent journal.
	j, err := a.Store.InitializeLifecycleJournal(t.Context(), a.Store.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	a.Config.Lifecycle = j
	user, err := a.Auth.Register(t.Context(), auth.Registration{Email: "lifecycle@example.test", Username: "lifecycle", FirstName: "Portable", LastName: "Fixture", Password: lifecyclePassword})
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.Auth.Rotate(t.Context(), &user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	return a, a.Handler(nil), user, token
}
func lifecycleRequest(h http.Handler, path, body, token, csrf, origin, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Idempotency-Key", key)
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "mychef_session", Value: token})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestAccountExportRequiresPasswordAndStreamsPrivateZIP(t *testing.T) {
	a, h, user, token := lifecycleHTTPFixture(t)
	body := credentialJSON(t, map[string]string{"currentPassword": lifecyclePassword})
	for _, tc := range []struct {
		name, body, token, csrf, origin string
		status                          int
	}{
		{"signed out", body, "", "", a.Config.Origin, 401},
		{"csrf", body, token, "wrong", a.Config.Origin, 403},
		{"origin", body, token, auth.CSRF(token), "https://foreign.test", 403},
		{"wrong password", credentialJSON(t, map[string]string{"currentPassword": "wrong"}), token, auth.CSRF(token), a.Config.Origin, 400},
		{"missing password", "{}", token, auth.CSRF(token), a.Config.Origin, 400},
		{"unknown field", strings.TrimSuffix(body, "}") + `,"userId":"999"}`, token, auth.CSRF(token), a.Config.Origin, 400},
		{"duplicate key", `{"currentPassword":"first","currentPassword":"second"}`, token, auth.CSRF(token), a.Config.Origin, 400},
		{"malformed Unicode", `{"currentPassword":"\ud800"}`, token, auth.CSRF(token), a.Config.Origin, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := lifecycleRequest(h, exportPath, tc.body, tc.token, tc.csrf, tc.origin, "")
			if w.Code != tc.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
	w := lifecycleRequest(h, exportPath, body, token, auth.CSRF(token), a.Config.Origin, "")
	if w.Code != 200 {
		t.Fatalf("export: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("unsafe ZIP headers", w.Header())
	}
	if !regexp.MustCompile(`^attachment; filename="mychef-data-[0-9a-f]{32}\.zip"$`).MatchString(w.Header().Get("Content-Disposition")) {
		t.Fatal("unsafe attachment filename")
	}
	if w.Header().Get("X-MyChef-User") != fmt.Sprint(user.ID) {
		t.Fatal("missing acting account")
	}
	archive, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) < 10 {
		t.Fatal("missing export inventory")
	}
	for _, f := range archive.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(user.Password)) || bytes.Contains(data, []byte(token)) {
			t.Fatal("export contained credential")
		}
	}
	if _, err := a.Auth.Load(t.Context(), token); err != nil {
		t.Fatal("export revoked current session")
	}
}
func TestAccountDeletionStrictConfirmationAndSessionRevocation(t *testing.T) {
	a, h, user, token := lifecycleHTTPFixture(t)
	body := credentialJSON(t, map[string]string{"currentPassword": lifecyclePassword, "confirmation": "DELETE"})
	key := strings.Repeat("a", 32)
	for _, tc := range []struct{ body, key string }{
		{body, ""}, {body, "not-a-key"},
		{credentialJSON(t, map[string]string{"currentPassword": lifecyclePassword, "confirmation": "delete"}), key},
		{credentialJSON(t, map[string]string{"currentPassword": lifecyclePassword, "confirmation": " DELETE "}), key},
		{credentialJSON(t, map[string]string{"currentPassword": "wrong", "confirmation": "DELETE"}), key},
	} {
		w := lifecycleRequest(h, deletePath, tc.body, token, auth.CSRF(token), a.Config.Origin, tc.key)
		if w.Code != 400 {
			t.Fatalf("invalid delete: %d %s", w.Code, w.Body.String())
		}
		var n int64
		if err := a.Store.DB.Model(&store.User{}).Where("user_id=?", user.ID).Count(&n).Error; err != nil || n != 1 {
			t.Fatal("rejected deletion changed account", err)
		}
	}
	other, err := a.Auth.Rotate(t.Context(), &user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	w := lifecycleRequest(h, deletePath, body, token, auth.CSRF(token), a.Config.Origin, key)
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result["status"] != "deleted" {
		t.Fatal("incorrect deletion result")
	}
	expired := false
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "mychef_session" && cookie.MaxAge < 0 {
			expired = true
		}
	}
	if !expired {
		t.Fatal("session cookie was not cleared")
	}
	for _, value := range []string{token, other} {
		if _, err := a.Auth.Load(t.Context(), value); err == nil {
			t.Fatal("deleted session survived")
		}
	}
	if _, err := a.Auth.Login(t.Context(), user.Email, lifecyclePassword); err == nil {
		t.Fatal("deleted account logged in")
	}
	replay := lifecycleRequest(h, deletePath, body, token, auth.CSRF(token), a.Config.Origin, key)
	if replay.Code != 401 {
		t.Fatal("revoked session replay was authenticated", replay.Code)
	}
	var notices int64
	if err := a.Store.SQL.QueryRow("SELECT COUNT(*) FROM email_outbox").Scan(&notices); err != nil || notices != 0 {
		t.Fatal("disabled notice queued personal mail", err)
	}
}
func TestUnverifiedAccountCanOnlyUseOwnLifecycleActions(t *testing.T) {
	a, _, _, token := lifecycleHTTPFixture(t)
	a.Config.Verification = &auth.EmailVerification{}
	h := a.Handler(nil)
	if w := call(h, "GET", "/api/v1/profile", "", token, "", a.Config.Origin); w.Code != 403 {
		t.Fatal("unverified profile restriction lost")
	}
	body := credentialJSON(t, map[string]string{"currentPassword": lifecyclePassword})
	if w := lifecycleRequest(h, exportPath, body, token, auth.CSRF(token), a.Config.Origin, ""); w.Code != 200 {
		t.Fatal("unverified export blocked", w.Code, w.Body.String())
	}
	body = credentialJSON(t, map[string]string{"currentPassword": lifecyclePassword, "confirmation": "DELETE"})
	if w := lifecycleRequest(h, deletePath, body, token, auth.CSRF(token), a.Config.Origin, strings.Repeat("c", 32)); w.Code != 200 {
		t.Fatal("unverified deletion blocked", w.Code, w.Body.String())
	}
}
func TestLifecyclePasswordFailuresAreThrottled(t *testing.T) {
	a, h, _, token := lifecycleHTTPFixture(t)
	now := time.Now()
	a.passwordLimits.now = func() time.Time { return now }
	body := credentialJSON(t, map[string]string{"currentPassword": "wrong"})
	for i := 0; i < 5; i++ {
		if w := lifecycleRequest(h, exportPath, body, token, auth.CSRF(token), a.Config.Origin, ""); w.Code != 400 {
			t.Fatal("wrong-password attempt", w.Code)
		}
	}
	w := lifecycleRequest(h, exportPath, body, token, auth.CSRF(token), a.Config.Origin, "")
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("missing password limit", w.Code)
	}
}

type failedAccountTransfer struct{ *httptest.ResponseRecorder }

func (w failedAccountTransfer) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestAccountExportInterruptAbortsTransferWithoutJSONOrArchive(t *testing.T) {
	a, h, _, token := lifecycleHTTPFixture(t)
	body := credentialJSON(t, map[string]string{"currentPassword": lifecyclePassword})
	r := httptest.NewRequest("POST", exportPath, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", a.Config.Origin)
	r.Header.Set("X-CSRF-Token", auth.CSRF(token))
	r.AddCookie(&http.Cookie{Name: "mychef_session", Value: token})
	writer := failedAccountTransfer{httptest.NewRecorder()}
	var failure any
	func() { defer func() { failure = recover() }(); h.ServeHTTP(writer, r) }()
	if failure != http.ErrAbortHandler {
		t.Fatal("incomplete export was allowed to complete a clean HTTP transfer")
	}
	if writer.Body.Len() != 0 {
		t.Fatal("failed export appended a body")
	}
	if _, err := a.Auth.Load(t.Context(), token); err != nil {
		t.Fatal("interrupted export changed the account")
	}
}
func TestAccountDeletionConcurrentRequestsCommitOneFinalNotice(t *testing.T) {
	a, _, user, token := lifecycleHTTPFixture(t)
	a.Config.AccountMail = true
	if err := a.Store.DB.Model(&store.User{}).Where("user_id=?", user.ID).Update("email_verified_at", time.Now().Unix()).Error; err != nil {
		t.Fatal(err)
	}
	h := a.Handler(nil)
	body := credentialJSON(t, map[string]string{"currentPassword": lifecyclePassword, "confirmation": "DELETE"})
	results := make(chan int, 2)
	gate := make(chan struct{})
	for range 2 {
		go func() {
			<-gate
			results <- lifecycleRequest(h, deletePath, body, token, auth.CSRF(token), a.Config.Origin, strings.Repeat("e", 32)).Code
		}()
	}
	close(gate)
	codes := []int{<-results, <-results}
	successes := 0
	for _, code := range codes {
		if code == 200 {
			successes++
		} else if code != 401 {
			t.Fatal("unexpected concurrent deletion response", codes)
		}
	}
	if successes != 1 {
		t.Fatal("duplicate deletion did not have one winner", codes)
	}
	var n int
	if err := a.Store.SQL.QueryRow("SELECT COUNT(*) FROM email_outbox WHERE template=?", store.AccountDeletedTemplate).Scan(&n); err != nil || n != 1 {
		t.Fatal("final notice duplicated or missing", err)
	}
	var recipient, data string
	if err := a.Store.SQL.QueryRow("SELECT recipient,data FROM email_outbox WHERE template=?", store.AccountDeletedTemplate).Scan(&recipient, &data); err != nil || recipient != user.Email || strings.Contains(data, lifecyclePassword) || strings.Contains(data, "proof") || strings.Contains(data, "link") {
		t.Fatal("unsafe final notice", err)
	}
	var previous string
	if err := a.Store.SQL.QueryRow("SELECT incarnation FROM lifecycle_operations").Scan(&previous); err != nil {
		t.Fatal(err)
	}
	fresh, err := a.Auth.Register(t.Context(), auth.Registration{Email: user.Email, Username: "fresh", FirstName: "Fresh", LastName: "Account", Password: lifecyclePassword})
	if err != nil {
		t.Fatal("same address could not start fresh", err)
	}
	var incarnation string
	if err := a.Store.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=?", fresh.ID).Scan(&incarnation); err != nil || incarnation == previous {
		t.Fatal("fresh account reused deleted identity", err)
	}
}

func TestAccountProfileInstanceSeparatesAReusedNumericID(t *testing.T) {
	a, h, user, token := lifecycleHTTPFixture(t)
	readInstance := func(token string) string {
		t.Helper()
		w := call(h, "GET", "/api/v1/session", "", token, "", "")
		var value struct {
			User struct {
				Instance string `json:"accountInstance"`
			} `json:"user"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil || len(value.User.Instance) != 32 {
			t.Fatal("signed-in profile lacks its account incarnation", err)
		}
		return value.User.Instance
	}
	before := readInstance(token)
	body := credentialJSON(t, map[string]string{"currentPassword": lifecyclePassword, "confirmation": "DELETE"})
	if w := lifecycleRequest(h, deletePath, body, token, auth.CSRF(token), a.Config.Origin, strings.Repeat("f", 32)); w.Code != 200 {
		t.Fatal("fixture deletion failed")
	}
	// A snapshot taken before this account existed has an older allocation sequence.
	if err := a.Store.DB.Exec("DELETE FROM sqlite_sequence WHERE name='users'").Error; err != nil {
		t.Fatal(err)
	}
	fresh, err := a.Auth.Register(t.Context(), auth.Registration{Email: user.Email, Username: "freshinstance", FirstName: "Fresh", LastName: "Instance", Password: lifecyclePassword})
	if err != nil || fresh.ID != user.ID {
		t.Fatal("fixture did not exercise a reused numeric ID", err)
	}
	next, err := a.Auth.Rotate(t.Context(), &fresh.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if after := readInstance(next); after == before {
		t.Fatal("different accounts shared a browser cache identity")
	}
}

type blockedAccountGenerator struct {
	start func(context.Context, int64) (domain.Operation, error)
}

func (g blockedAccountGenerator) Start(ctx context.Context, user int64, _ string, _ domain.MenuInput) (domain.Operation, error) {
	return g.start(ctx, user)
}
func (g blockedAccountGenerator) Status(context.Context, int64, int64) (domain.Operation, error) {
	return domain.Operation{}, nil
}
func TestAccountDeletionWaitsForPreviouslyAuthenticatedWrites(t *testing.T) {
	a, _, user, token := lifecycleHTTPFixture(t)
	started, release, requestDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	released := false
	unblock := func() {
		if !released {
			close(release)
			released = true
		}
	}
	var response *httptest.ResponseRecorder
	a.Generator = blockedAccountGenerator{start: func(ctx context.Context, id int64) (domain.Operation, error) {
		close(started)
		<-release
		err := a.Store.DB.WithContext(ctx).Model(&store.User{}).Where("user_id=?", id).Update("firstname", "Old authenticated update").Error
		return domain.Operation{}, err
	}}
	h := a.Handler(nil)
	go func() {
		response = lifecycleRequest(h, "/api/v1/generations", "{}", token, auth.CSRF(token), a.Config.Origin, strings.Repeat("1", 32))
		close(requestDone)
	}()
	<-started
	defer func() { unblock(); <-requestDone }()
	deleted := make(chan struct{})
	var deleteErr error
	go func() {
		_, deleteErr = a.Config.Lifecycle.DeleteAccount(t.Context(), store.AccountActor{UserID: user.ID, SessionHash: auth.Hash(token), PasswordHash: user.Password}, strings.Repeat("2", 32), "DELETE", time.Now(), store.AccountDeletionOptions{})
		close(deleted)
	}()
	select {
	case <-deleted:
		t.Fatal("deletion completed while a previously authenticated write was still pending", deleteErr)
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	<-requestDone
	if response.Code != 202 {
		t.Fatal("existing request did not complete before deletion", response.Code)
	}
	select {
	case <-deleted:
	case <-time.After(5 * time.Second):
		t.Fatal("deletion did not resume")
	}
	if deleteErr != nil {
		t.Fatal(deleteErr)
	}
	var n int64
	if err := a.Store.DB.Model(&store.User{}).Count(&n).Error; err != nil || n != 0 {
		t.Fatal("old request recreated account data", err)
	}
}
