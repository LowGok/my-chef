package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/store"
)

func feedbackSession(t *testing.T, a *API, name string, verified bool) (int64, string) {
	t.Helper()
	u := store.User{Email: name + "@example.test", Username: name, FirstName: "Test", LastName: "Cook", Password: "fixture"}
	if verified {
		u.EmailVerifiedAt = store.Ptr(time.Now().Unix())
	}
	if err := a.Store.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	token, err := a.Auth.Rotate(t.Context(), &u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	return u.ID, token
}

func feedbackResponse(t *testing.T, w *httptest.ResponseRecorder, want int, code string) {
	t.Helper()
	if w.Code != want || !strings.Contains(w.Body.String(), code) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected %d %q with no-store, got %d", want, code, w.Code)
	}
}

func TestFeedbackVerifiedRoutesAndReceipts(t *testing.T) {
	const path = "/api/v1/feedback"
	a, h := testAPI(t)
	var diagnostics bytes.Buffer
	a.Logger = slog.New(slog.NewJSONHandler(&diagnostics, nil))
	id, token := feedbackSession(t, a, "cook", true)
	_, other := feedbackSession(t, a, "other", true)
	_, unverified := feedbackSession(t, a, "unverified", false)
	request := func(method, url, body, session string) *httptest.ResponseRecorder {
		return call(h, method, url, body, session, auth.CSRF(session), a.Config.Origin)
	}
	feedbackResponse(t, request("GET", path+"/availability", "", token), 503, "feedback_unavailable")
	a.Config.Verification = &auth.EmailVerification{}
	a.Config.FeedbackTo = "operator@example.test"
	h = a.Handler(nil)
	feedbackResponse(t, request("GET", path+"/availability", "", ""), 401, "unauthenticated")
	feedbackResponse(t, request("GET", path+"/availability", "", unverified), 403, "email_verification_required")
	feedbackResponse(t, request("GET", path+"/availability", "", token), 200, `"enabled":true`)

	res, err := a.Store.SQL.Exec("INSERT INTO meals(creator_id,creationdate,recipetitle) VALUES(?,'2026-11-01','Synthetic meal')", id)
	if err != nil {
		t.Fatal(err)
	}
	meal, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	report := func(i int, topic, message string, recipe bool) string {
		input := map[string]string{"requestKey": fmt.Sprintf("%032x", i), "topic": topic, "message": message}
		if recipe {
			input["recipeId"] = fmt.Sprint(meal)
		}
		return credentialJSON(t, input)
	}
	message := strings.Repeat("🫛", 20)
	body := report(1, "technical_issue", message, false)
	feedbackResponse(t, call(h, "POST", path, body, token, auth.CSRF(token), "https://other.test"), 403, "csrf_invalid")
	feedbackResponse(t, call(h, "POST", path, body, token, "", a.Config.Origin), 403, "csrf_invalid")
	for _, invalid := range []string{
		report(1, "technical_issue", message, true),
		report(1, "subscription", message, true),
		report(1, "other", message, true),
		report(1, "technical_issue", strings.Repeat("🫛", 19), false),
		report(1, "technical_issue", strings.Repeat("🫛", 4001), false),
		strings.Replace(body, "🫛", "\xff", 1),
		strings.Replace(body, "🫛", `\ud800`, 1),
		strings.TrimSuffix(body, "}") + `,"priority":"premium"}`,
		strings.TrimSuffix(body, "}") + `,"contactEmail":"forged@example.test"}`,
		strings.TrimSuffix(body, "}") + `,"message":"duplicate"}`,
	} {
		feedbackResponse(t, request("POST", path, invalid, token), 400, "invalid_input")
	}
	for _, retired := range []string{"app_improvement", "minor_app_bug", "critical_app_bug", "major_recipe_issue", "critical_recipe_issue"} {
		feedbackResponse(t, request("POST", path, report(1, retired, message, false), token), 400, "invalid_input")
	}
	feedbackResponse(t, request("POST", path, report(1, "recipe_issue", message, true), other), 404, "not_found")
	first := request("POST", path, body, token)
	feedbackResponse(t, first, 200, "reference")
	var receipt map[string]string
	if json.Unmarshal(first.Body.Bytes(), &receipt) != nil || len(receipt) != 2 || receipt["reference"] == "" || receipt["createdAt"] == "" {
		t.Fatal("receipt exposed more than reference and time")
	}
	for i, topic := range []string{"recipe_issue", "subscription", "other", "recipe_issue"} {
		feedbackResponse(t, request("POST", path, report(i+2, topic, strings.Repeat("🫛", 4000), i == 0), token), 200, "reference")
	}
	rejected := request("POST", path, report(6, "technical_issue", message, false), token)
	feedbackResponse(t, rejected, 429, "rate_limited")
	if rejected.Header().Get("Retry-After") == "" {
		t.Fatal("limit response omitted retry interval")
	}
	if replay := request("POST", path, body, token); replay.Code != 200 || replay.Body.String() != first.Body.String() {
		t.Fatal("quota interrupted an exact replay")
	}
	feedbackResponse(t, request("POST", path, report(1, "subscription", message, false), token), 409, "conflict")

	a.Config.FeedbackTo = ""
	feedbackResponse(t, request("GET", path+"/availability", "", token), 200, `"enabled":false`)
	feedbackResponse(t, request("POST", path, body, other), 503, "feedback_unavailable")
	lookup := `{"operation":"feedback","requestKey":"00000000000000000000000000000001"}`
	feedbackResponse(t, request("POST", path+"/receipt", lookup, other), 404, "not_found")
	for _, replay := range []*httptest.ResponseRecorder{request("POST", path, body, token), request("POST", path+"/receipt", lookup, token)} {
		if replay.Code != 200 || replay.Body.String() != first.Body.String() {
			t.Fatal("missing mailbox interrupted receipt recovery")
		}
	}
	rating := fmt.Sprintf("/api/v1/recipes/%d/rating", meal)
	feedbackResponse(t, request("GET", rating, "", token), 200, `"rating":null`)
	feedbackResponse(t, request("GET", rating, "", other), 404, "not_found")
	feedbackResponse(t, request("PUT", rating, `{"requestKey":"00000000000000000000000000000001","rating":5}`, token), 200, "reference")
	feedbackResponse(t, request("GET", rating, "", token), 200, `"rating":5`)
	feedbackResponse(t, request("PUT", rating, `{"requestKey":"00000000000000000000000000000002","rating":5}`, token), 409, "rating_unchanged")
	var notices, reports int
	if err := a.Store.SQL.QueryRow("SELECT (SELECT count(*) FROM email_outbox), (SELECT count(*) FROM feedback_reports)").Scan(&notices, &reports); err != nil || reports != 5 || notices != reports {
		t.Fatal("unexpected writes from rejected reports, replays or ratings")
	}
	// Force an error in the real storage path. Neither a sensitive SQL value
	// nor the provider settings should be written to the response or logs.
	if _, err := a.Store.SQL.Exec("ALTER TABLE feedback_reports RENAME TO broken_feedback_reports"); err != nil {
		t.Fatal(err)
	}
	a.Config.FeedbackTo = "operator@example.test"
	const canary = "Synthetic feedback log canary 01."
	rejected = request("POST", path, report(7, "technical_issue", canary, false), token)
	feedbackResponse(t, rejected, 503, "storage_unavailable")
	if !strings.Contains(diagnostics.String(), "requestId") {
		t.Fatal("storage failure did not log a safe request ID")
	}
	for _, value := range []string{canary, "cook@example.test", "operator@example.test", token} {
		if strings.Contains(diagnostics.String(), value) || strings.Contains(rejected.Body.String(), value) {
			t.Fatal("feedback diagnostics or API error exposed a private canary")
		}
	}

}
