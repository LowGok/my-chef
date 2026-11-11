package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"image"
	"image/png"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/imports"
	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
)

type httpImportWorker struct {
	pages, extractions atomic.Int64
	unavailable        atomic.Bool
	block              chan struct{}
	title              string
	readyProbe         func()
}

func (w *httpImportWorker) Ready(context.Context) (imports.WorkerReady, error) {
	if w.readyProbe != nil {
		w.readyProbe()
	}
	if w.unavailable.Load() {
		return imports.WorkerReady{}, imports.ErrExtractionUnavailable
	}
	return imports.WorkerReady{Ready: true, AIAvailable: true, Fixture: true}, nil
}
func (w *httpImportWorker) CheckPage(ctx context.Context, input imports.WorkerRequest) (imports.WorkerResult, error) {
	w.pages.Add(1)
	if w.block != nil {
		select {
		case <-w.block:
		case <-ctx.Done():
			return imports.WorkerResult{}, ctx.Err()
		}
	}
	html := []byte(`<script type="application/ld+json">{"@type":"Recipe","name":"Private imported soup","recipeYield":"4 servings","prepTime":"PT5M","cookTime":"PT10M","totalTime":"PT20M","recipeIngredient":["2 carrots"],"recipeInstructions":["Cook gently."]}</script>`)
	if w.title != "" {
		html = bytes.ReplaceAll(html, []byte("Private imported soup"), []byte(w.title))
	}
	if strings.HasSuffix(input.Source.URL, "/plain") {
		html = []byte("<main>Ingredients: carrots. Instructions: cook gently.</main>")
	}
	inspection, err := imports.InspectRecipePage(imports.RecipePage{URL: input.Source.URL, HTML: html}, input.Source)
	return imports.WorkerResult{Private: imports.PrivateDraft{Source: input.Source, Inspection: &inspection}}, err
}
func (w *httpImportWorker) Extract(_ context.Context, input imports.WorkerRequest) (imports.WorkerResult, error) {
	w.extractions.Add(1)
	servings, prep, cook, total := 4, 5, 10, 20
	draft, err := imports.NewDraft(imports.ExtractedRecipe{Title: "Photo recipe", Servings: &servings, PrepMinutes: &prep, CookMinutes: &cook, TotalMinutes: &total, Ingredients: []imports.ExtractedIngredient{{Name: "Carrots", SourceText: "A handful"}}, Instructions: []string{"Cook gently."}}, input.Source)
	result := imports.PrivateDraft{Source: input.Source, Draft: &draft}
	return imports.WorkerResult{Private: result, Model: "gemini-3.1-flash-lite"}, err
}
func importHTTPFixture(t *testing.T) (*API, http.Handler, string, string, string, *httpImportWorker) {
	t.Helper()
	a, _, _, token, other, unverified, _, _ := personalHTTPFixture(t)
	vault, err := imports.NewDraftVault(bytes.Repeat([]byte{71}, 32))
	if err != nil {
		t.Fatal(err)
	}
	spool, err := imports.NewImageSpool(filepath.Join(t.TempDir(), "sources"), imports.DefaultImageLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = spool.Close() })
	worker := &httpImportWorker{}
	service, err := NewRecipeImportService(t.Context(), a.Store, a.Config.Lifecycle, a.Config.PersonalEntitlements, vault, worker, spool, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	a.Config.RecipeImports = service
	return a, a.Handler(nil), token, other, unverified, worker
}
func readImportHTTP(t *testing.T, w *httptest.ResponseRecorder, want int) store.RecipeImport {
	t.Helper()
	if w.Code != want {
		t.Fatal("unexpected import status", w.Code, want)
	}
	var out store.RecipeImport
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.ID == "" {
		t.Fatal("invalid import response")
	}
	return out
}
func TestRecipeImportHTTPGuardsAndUnsavedStructuredDraft(t *testing.T) {
	a, h, token, free, unverified, worker := importHTTPFixture(t)
	body := `{"requestKey":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://recipes.example.com/soup","personalUseConfirmed":true}`
	for _, tc := range []struct {
		token, csrf, origin string
		want                int
	}{{"", "", a.Config.Origin, 401}, {unverified, auth.CSRF(unverified), a.Config.Origin, 403}, {free, auth.CSRF(free), a.Config.Origin, 403}, {token, "", a.Config.Origin, 403}, {token, auth.CSRF(token), "https://foreign.test", 403}} {
		w := call(h, "POST", "/api/v1/recipe-imports/url", body, tc.token, tc.csrf, tc.origin)
		if w.Code != tc.want || worker.pages.Load() != 0 || worker.extractions.Load() != 0 {
			t.Fatal("import authority failed before source work", w.Code)
		}
	}
	for _, bad := range []string{strings.Replace(body, `"personalUseConfirmed":true`, `"personalUseConfirmed":false`, 1), strings.TrimSuffix(body, "}") + `,"premium":true}`, strings.TrimSuffix(body, "}") + `,"model":"paid"}`} {
		w := call(h, "POST", "/api/v1/recipe-imports/url", bad, token, auth.CSRF(token), a.Config.Origin)
		if w.Code != 400 || worker.pages.Load() != 0 {
			t.Fatal("invalid import controls accepted", w.Code)
		}
	}
	first := readImportHTTP(t, call(h, "POST", "/api/v1/recipe-imports/url", body, token, auth.CSRF(token), a.Config.Origin), 202)
	path := "/api/v1/recipe-imports/" + first.ID
	var ready store.RecipeImport
	for range 100 {
		w := call(h, "GET", path, "", token, "", a.Config.Origin)
		ready = readImportHTTP(t, w, 200)
		if ready.Stage == "ready" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ready.Stage != "ready" || ready.Draft == nil || worker.pages.Load() != 1 || worker.extractions.Load() != 0 {
		t.Fatal("structured draft did not arrive without AI")
	}
	if w := call(h, "GET", path, "", free, "", a.Config.Origin); w.Code != 404 {
		t.Fatal("foreign draft readable", w.Code)
	}
	replay := readImportHTTP(t, call(h, "POST", "/api/v1/recipe-imports/url", body, token, auth.CSRF(token), a.Config.Origin), 202)
	if replay.ID != first.ID || worker.pages.Load() != 1 {
		t.Fatal("duplicate import fetched again")
	}
	for _, table := range []string{"personal_recipes", "favorites", "shopping_lists", "meals"} {
		var count int64
		if err := a.Store.DB.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("import automatically saved or planned a recipe", table, err)
		}
	}
	if w := call(h, "POST", path+"/read-with-ai", `{"aiConsentConfirmed":true}`, token, auth.CSRF(token), a.Config.Origin); w.Code != 409 || worker.extractions.Load() != 0 {
		t.Fatal("complete structured recipe offered unnecessary AI", w.Code)
	}
}
func TestRecipeImportHTTPCancelFencesLateResult(t *testing.T) {
	a, h, token, _, _, worker := importHTTPFixture(t)
	worker.block = make(chan struct{})
	body := `{"requestKey":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","url":"https://recipes.example.com/soup","personalUseConfirmed":true}`
	first := readImportHTTP(t, call(h, "POST", "/api/v1/recipe-imports/url", body, token, auth.CSRF(token), a.Config.Origin), 202)
	path := "/api/v1/recipe-imports/" + first.ID
	if w := call(h, "POST", path+"/cancel", `{}`, token, auth.CSRF(token), a.Config.Origin); w.Code != 204 {
		t.Fatal("cancel failed", w.Code)
	}
	close(worker.block)
	got := readImportHTTP(t, call(h, "GET", path, "", token, "", a.Config.Origin), 200)
	if got.Stage != "cancelled" || got.Draft != nil {
		t.Fatal("cancel retained/recreated a draft")
	}
	var count int64
	if err := a.Store.DB.Table("recipe_import_operations").Where("cipher IS NOT NULL").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("cancel retained private content", err)
	}
}

func TestRecipeImportHTTPExplicitFallbackAndPhotoStreaming(t *testing.T) {
	a, h, token, _, _, worker := importHTTPFixture(t)
	body := `{"requestKey":"cccccccccccccccccccccccccccccccc","url":"https://recipes.example.com/plain","personalUseConfirmed":true}`
	op := readImportHTTP(t, call(h, "POST", "/api/v1/recipe-imports/url", body, token, auth.CSRF(token), a.Config.Origin), 202)
	path := "/api/v1/recipe-imports/" + op.ID
	for range 100 {
		op = readImportHTTP(t, call(h, "GET", path, "", token, "", a.Config.Origin), 200)
		if op.Stage == "awaiting-ai" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if op.Stage != "awaiting-ai" || !op.CanReadWithAI || worker.extractions.Load() != 0 {
		t.Fatal("fallback ran automatically")
	}
	if w := call(h, "POST", path+"/read-with-ai", `{}`, token, auth.CSRF(token), a.Config.Origin); w.Code != 400 || worker.extractions.Load() != 0 {
		t.Fatal("AI consent bypassed")
	}
	readImportHTTP(t, call(h, "POST", path+"/read-with-ai", `{"aiConsentConfirmed":true}`, token, auth.CSRF(token), a.Config.Origin), 202)
	for range 100 {
		op = readImportHTTP(t, call(h, "GET", path, "", token, "", a.Config.Origin), 200)
		if op.Stage == "ready" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if op.Stage != "ready" || worker.extractions.Load() != 1 {
		t.Fatal("explicit fallback failed")
	}
	a.Config.RecipeImports.normalize = func(_ context.Context, raw []byte) ([]byte, error) {
		photo, err := providers.NormalizeImportPhoto(raw)
		return photo.JPEG(), err
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 20, 20))); err != nil {
		t.Fatal(err)
	}
	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	if err := form.WriteField("import", `{"requestKey":"dddddddddddddddddddddddddddddddd","personalUseConfirmed":true,"aiConsentConfirmed":true}`); err != nil {
		t.Fatal(err)
	}
	part, err := form.CreateFormFile("photos", "private-source-filename-canary.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(pngData.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	photo := readImportHTTP(t, callPersonalPhoto(h, "POST", "/api/v1/recipe-imports/photos", upload.Bytes(), form.FormDataContentType(), token, a.Config.Origin), 202)
	photoPath := "/api/v1/recipe-imports/" + photo.ID
	for range 100 {
		photo = readImportHTTP(t, call(h, "GET", photoPath, "", token, "", a.Config.Origin), 200)
		if photo.Stage == "ready" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if photo.Stage != "ready" || photo.Draft == nil || photo.Draft.Source.Kind != "photos" || worker.extractions.Load() != 2 {
		t.Fatal("photo draft failed")
	}
	var model, provider string
	if err := a.Store.SQL.QueryRow("SELECT provider,model FROM recipe_import_operations WHERE id=?", photo.ID).Scan(&provider, &model); err != nil || provider != "fixture" || model != "gemini-3.1-flash-lite" {
		t.Fatal("dispatch metadata missing", err)
	}
}

func TestRecipeImportHTTPReviewedSaveHandoff(t *testing.T) {
	a, h, token, _, _, _ := importHTTPFixture(t)
	op := readImportHTTP(t, call(h, "POST", "/api/v1/recipe-imports/url", `{"requestKey":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","url":"https://recipes.example.com/soup","personalUseConfirmed":true}`, token, auth.CSRF(token), a.Config.Origin), 202)
	path := "/api/v1/recipe-imports/" + op.ID
	for range 100 {
		op = readImportHTTP(t, call(h, "GET", path, "", token, "", a.Config.Origin), 200)
		if op.Stage == "ready" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if op.Draft == nil {
		t.Fatal("missing handoff draft")
	}
	body, err := json.Marshal(map[string]any{"content": op.Draft.Content})
	if err != nil {
		t.Fatal(err)
	}
	response := call(h, "POST", path+"/review", string(body), token, auth.CSRF(token), a.Config.Origin)
	if response.Code != 200 {
		t.Fatal("review snapshot unavailable", response.Code)
	}
	var review imports.ReviewSnapshot
	if json.Unmarshal(response.Body.Bytes(), &review) != nil {
		t.Fatal("invalid review snapshot")
	}
	resolved := []string{}
	for _, issue := range op.Draft.Issues {
		resolved = append(resolved, issue.Field)
	}
	confirmation := imports.ReviewConfirmation{DraftRevision: op.Draft.Revision, ContentRevision: review.ContentRevision, ReviewedFields: review.RequiredFields, ResolvedFields: resolved}
	payload := map[string]any{"requestKey": strings.Repeat("f", 32), "content": review.Content, "photoAction": "keep", "import": map[string]any{"operationId": op.ID, "confirmation": confirmation}}
	body, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	badConfirmation := confirmation
	badConfirmation.DraftRevision = strings.Repeat("0", 64)
	bad := map[string]any{"requestKey": strings.Repeat("f", 32), "content": review.Content, "photoAction": "keep", "import": map[string]any{"operationId": op.ID, "confirmation": badConfirmation}}
	invalid, err := json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	if w := call(h, "POST", "/api/v1/personal-recipes", string(invalid), token, auth.CSRF(token), a.Config.Origin); w.Code != 409 {
		t.Fatal("stale review was accepted", w.Code)
	}
	bad["import"] = map[string]any{"operationId": op.ID, "confirmation": confirmation, "source": map[string]string{"url": "https://foreign.test"}}
	invalid, err = json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	if w := call(h, "POST", "/api/v1/personal-recipes", string(invalid), token, auth.CSRF(token), a.Config.Origin); w.Code != 400 {
		t.Fatal("client supplied provenance was accepted", w.Code)
	}
	saved := readHTTPPersonal(t, call(h, "POST", "/api/v1/personal-recipes", string(body), token, auth.CSRF(token), a.Config.Origin), 201)
	if saved.ImportSource == nil || saved.ImportSource.URL != "https://recipes.example.com/soup" || saved.ImageURL != nil {
		t.Fatal("source link or fallback was lost")
	}
	replay := readHTTPPersonal(t, call(h, "POST", "/api/v1/personal-recipes", string(body), token, auth.CSRF(token), a.Config.Origin), 201)
	if saved.ID != replay.ID || saved.VersionID != replay.VersionID {
		t.Fatal("HTTP save retry duplicated import")
	}
}

func TestRecipeImportHTTPRevocationPurgesFailedSource(t *testing.T) {
	a, h, token, _, _, worker := importHTTPFixture(t)
	worker.block = make(chan struct{})
	op := readImportHTTP(t, call(h, "POST", "/api/v1/recipe-imports/url", `{"requestKey":"11111111111111111111111111111111","url":"https://recipes.example.com/soup","personalUseConfirmed":true}`, token, auth.CSRF(token), a.Config.Origin), 202)
	for range 100 {
		if worker.pages.Load() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if worker.pages.Load() != 1 {
		t.Fatal("worker did not start")
	}
	if _, err := a.Store.SQL.Exec("DELETE FROM sessions WHERE hash=?", auth.Hash(token)); err != nil {
		t.Fatal(err)
	}
	close(worker.block)
	var stage string
	var cleared bool
	for range 100 {
		if err := a.Store.SQL.QueryRow("SELECT stage,cipher IS NULL FROM recipe_import_operations WHERE id=?", op.ID).Scan(&stage, &cleared); err != nil {
			t.Fatal(err)
		}
		if stage == "failed" && cleared {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("revoked import retained its in-flight source instead of failing privately")
}

func TestRecipeImportHTTPReplayDoesNotNeedAvailableWorker(t *testing.T) {
	a, h, token, _, _, worker := importHTTPFixture(t)
	body := `{"requestKey":"22222222222222222222222222222222","url":"https://recipes.example.com/soup","personalUseConfirmed":true}`
	op := readImportHTTP(t, call(h, "POST", "/api/v1/recipe-imports/url", body, token, auth.CSRF(token), a.Config.Origin), 202)
	for range 100 {
		op = readImportHTTP(t, call(h, "GET", "/api/v1/recipe-imports/"+op.ID, "", token, "", a.Config.Origin), 200)
		if op.Stage == "ready" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if op.Stage != "ready" {
		t.Fatal("source did not finish")
	}
	worker.unavailable.Store(true)
	replay := readImportHTTP(t, call(h, "POST", "/api/v1/recipe-imports/url", body, token, auth.CSRF(token), a.Config.Origin), 202)
	if replay.ID != op.ID || replay.Stage != "ready" || worker.pages.Load() != 1 {
		t.Fatal("worker outage hid or restarted a recorded import")
	}
	changed := strings.Replace(body, "/soup", "/different", 1)
	if w := call(h, "POST", "/api/v1/recipe-imports/url", changed, token, auth.CSRF(token), a.Config.Origin); w.Code != 409 {
		t.Fatal("replay accepted another source", w.Code)
	}
}

func TestRecipeImportHTTPPrivateContentStaysOutOfDiagnostics(t *testing.T) {
	a, h, token, foreign, _, worker := importHTTPFixture(t)
	marker := "private-" + rand.Text()
	worker.title = marker
	var logs bytes.Buffer
	a.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	a.Config.TraceDir = t.TempDir()
	body, err := json.Marshal(map[string]any{"requestKey": strings.Repeat("7", 32), "url": "https://recipes.example.com/soup?utm_source=" + marker, "personalUseConfirmed": true})
	if err != nil {
		t.Fatal(err)
	}
	op := readImportHTTP(t, call(h, "POST", "/api/v1/recipe-imports/url", string(body), token, auth.CSRF(token), a.Config.Origin), 202)
	path := "/api/v1/recipe-imports/" + op.ID
	for range 100 {
		op = readImportHTTP(t, call(h, "GET", path, "", token, "", a.Config.Origin), 200)
		if op.Stage == "ready" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if op.Draft == nil || op.Draft.Content.Title != marker || strings.Contains(op.Draft.Source.URL, marker) {
		t.Fatal("private handoff or source canonicalization failed")
	}
	for _, response := range []*httptest.ResponseRecorder{
		call(h, "GET", path, "", foreign, "", a.Config.Origin),
		call(h, "POST", "/api/v1/recipe-imports/url", strings.Replace(string(body), "utm_source=", "token=", 1), token, auth.CSRF(token), a.Config.Origin),
	} {
		if response.Code < 400 || bytes.Contains(response.Body.Bytes(), []byte(marker)) {
			t.Fatal("private content escaped through a rejected response")
		}
	}
	if w := call(h, "POST", path+"/cancel", "{}", token, auth.CSRF(token), a.Config.Origin); w.Code != 204 {
		t.Fatal("cleanup failed")
	}
	var retained int
	if err := a.Store.SQL.QueryRow("SELECT COUNT(*) FROM recipe_import_operations WHERE id=? AND cipher IS NOT NULL", op.ID).Scan(&retained); err != nil || retained != 0 {
		t.Fatal("discard retained private draft", err)
	}
	if bytes.Contains(logs.Bytes(), []byte(marker)) {
		t.Fatal("private content reached diagnostics")
	}
	if err := filepath.WalkDir(a.Config.TraceDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(marker)) {
			t.Error("private content reached a retained trace")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecipeImportHTTPDoesNotHoldDeletionFenceDuringWorkerIO(t *testing.T) {
	a, h, token, _, _, worker := importHTTPFixture(t)
	worker.readyProbe = func() {
		if !a.Store.AccountWork.TryLock() {
			t.Error("worker readiness held the deletion fence, allowing nested reader deadlock")
			return
		}
		a.Store.AccountWork.Unlock()
	}
	body := `{"requestKey":"99999999999999999999999999999999","url":"https://recipes.example.com/soup","personalUseConfirmed":true}`
	readImportHTTP(t, call(h, "POST", "/api/v1/recipe-imports/url", body, token, auth.CSRF(token), a.Config.Origin), 202)
}
