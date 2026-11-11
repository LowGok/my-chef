package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/menus"
	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

type httpPhotoNormalizer struct{}

func (httpPhotoNormalizer) Normalize(_ context.Context, data []byte) (providers.NormalizedPhoto, error) {
	return providers.NormalizePersonalPhoto(data)
}

func personalHTTPFixture(t *testing.T) (*API, http.Handler, int64, string, string, string, *providers.FixturePersonalPhotos, *bytes.Buffer) {
	t.Helper()
	a, _ := testAPI(t)
	owner, token := feedbackSession(t, a, "personal-owner", true)
	_, other := feedbackSession(t, a, "personal-other", true)
	_, unverified := feedbackSession(t, a, "personal-unverified", false)
	j, err := a.Store.InitializeLifecycleJournal(t.Context(), a.Store.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	reader := func(_ context.Context, _ *gorm.DB, user int64, _ string) (store.PersonalEntitlement, error) {
		return store.PersonalEntitlement{Premium: user == owner, ExpiresUnix: time.Now().Add(time.Hour).Unix()}, nil
	}
	photos := providers.NewFixturePersonalPhotos()
	a.Config.Lifecycle = j
	a.Config.PersonalEntitlements = reader
	a.Config.PersonalPhotos = photos
	a.Config.PersonalRecipes = menus.NewPersonalRecipes(a.Store, j, reader, httpPhotoNormalizer{}, photos)
	var diagnostics bytes.Buffer
	a.Logger = slog.New(slog.NewJSONHandler(&diagnostics, nil))
	return a, a.Handler(nil), owner, token, other, unverified, photos, &diagnostics
}

func httpPersonalDocument() domain.PersonalRecipeContent {
	quantity := domain.Decimal("12345678901234567890.00000000001")
	return domain.PersonalRecipeContent{SchemaVersion: 1, Title: "HTTP-PRIVATE-RECIPE-CANARY", Description: "A private description.", Servings: 2, PrepMinutes: 5, CookMinutes: 10, TotalMinutes: 20, Ingredients: []domain.IngredientContent{{Name: "Carrot", Quantity: &quantity}}, Instructions: []string{"HTTP-PRIVATE-INSTRUCTION-CANARY"}, Tips: []string{}, Allergens: []string{}}
}
func personalRequestBody(t *testing.T, key string, content domain.PersonalRecipeContent, expected string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"requestKey": key, "expectedVersion": expected, "content": content, "photoAction": "keep"})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
func readHTTPPersonal(t *testing.T, w *httptest.ResponseRecorder, want int) store.PersonalRecipe {
	t.Helper()
	if w.Code != want {
		t.Fatalf("personal response status %d, expected %d", w.Code, want)
	}
	var recipe store.PersonalRecipe
	if json.Unmarshal(w.Body.Bytes(), &recipe) != nil || recipe.ID == "" {
		t.Fatal("invalid personal recipe response")
	}
	return recipe
}

func TestPersonalHTTPVerifiedOwnershipStrictFieldsAndVersionConflicts(t *testing.T) {
	a, h, _, token, other, unverified, _, diagnostics := personalHTTPFixture(t)
	request := func(method, path, body, session string) *httptest.ResponseRecorder {
		return call(h, method, path, body, session, auth.CSRF(session), a.Config.Origin)
	}
	const root = "/api/v1/personal-recipes"
	for _, tc := range []struct {
		session string
		allowed bool
	}{{token, true}, {other, false}} {
		response := request("GET", root+"/permissions", "", tc.session)
		var value struct {
			CanWrite            bool `json:"canWrite"`
			PhotoUploadsEnabled bool `json:"photoUploadsEnabled"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil || value.CanWrite != tc.allowed || !value.PhotoUploadsEnabled {
			t.Fatal("advisory permissions disagreed with server authority", response.Code)
		}
	}
	for _, tc := range []struct {
		token string
		want  int
	}{{"", 401}, {unverified, 403}} {
		if got := request("POST", root+"/search", `{}`, tc.token).Code; got != tc.want {
			t.Fatal("private search allowed restricted session", got)
		}
	}
	body := personalRequestBody(t, strings.Repeat("a", 32), httpPersonalDocument(), "")
	if got := call(h, "POST", root, body, token, auth.CSRF(token), "https://foreign.test").Code; got != 403 {
		t.Fatal("foreign Origin accepted")
	}
	if got := call(h, "POST", root, body, token, "", a.Config.Origin).Code; got != 403 {
		t.Fatal("missing CSRF accepted")
	}
	if got := request("POST", root, body, other).Code; got != 403 {
		t.Fatal("Free account created personal recipe")
	}
	for _, invalid := range []string{strings.TrimSuffix(body, "}") + `,"premium":true}`, strings.TrimSuffix(body, "}") + `,"ownerId":"forged"}`, strings.TrimSuffix(body, "}") + `,"imageUrl":"https://foreign.test/"}`, strings.Replace(body, "HTTP-PRIVATE-RECIPE-CANARY", `\ud800`, 1), strings.TrimSuffix(body, "}") + `,"requestKey":"duplicate"}`} {
		if got := request("POST", root, invalid, token).Code; got != 400 {
			t.Fatal("invalid/forged personal fields accepted", got)
		}
	}
	first := readHTTPPersonal(t, request("POST", root, body, token), 201)
	if replay := readHTTPPersonal(t, request("POST", root, body, token), 201); replay.VersionID != first.VersionID {
		t.Fatal("HTTP retry created another version")
	}
	path := root + "/" + first.ID
	if got := request("GET", path, "", other).Code; got != 404 {
		t.Fatal("foreign personal recipe readable", got)
	}
	if got := request("GET", path+"?version="+first.VersionID, "", other).Code; got != 404 {
		t.Fatal("foreign version readable", got)
	}
	search := request("POST", root+"/search", `{"query":"HTTP-PRIVATE-RECIPE-CANARY","limit":12}`, token)
	if search.Code != 200 || !strings.Contains(search.Body.String(), first.ID) {
		t.Fatal("private POST search failed")
	}
	if request("GET", root+"/search?query=HTTP-PRIVATE-RECIPE-CANARY", "", token).Code == 200 {
		t.Fatal("title search accepted in URL")
	}
	content := httpPersonalDocument()
	content.Title = "Updated private title"
	second := readHTTPPersonal(t, request("PUT", path, personalRequestBody(t, strings.Repeat("b", 32), content, first.VersionID), token), 200)
	conflict := request("PUT", path, personalRequestBody(t, strings.Repeat("c", 32), httpPersonalDocument(), first.VersionID), token)
	if conflict.Code != 409 || !strings.Contains(conflict.Body.String(), second.VersionID) || !strings.Contains(conflict.Body.String(), "changedFields") {
		t.Fatal("conflict omitted authorized current version/diff")
	}
	invalid := content
	invalid.Servings = 0
	validation := request("PUT", path, personalRequestBody(t, strings.Repeat("d", 32), invalid, second.VersionID), token)
	if validation.Code != 400 || !strings.Contains(validation.Body.String(), `"servings"`) {
		t.Fatal("field errors unavailable")
	}
	if strings.Contains(diagnostics.String(), "HTTP-PRIVATE-") {
		t.Fatal("private text entered diagnostics")
	}
}

func photoHTTPBody(t *testing.T, metadata string, raw []byte) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("recipe", metadata); err != nil {
		t.Fatal(err)
	}
	file, err := form.CreateFormFile("photo", "original-private-camera-name.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), form.FormDataContentType()
}
func callPersonalPhoto(h http.Handler, method, path string, body []byte, contentType, token, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("Origin", origin)
	r.Header.Set("X-CSRF-Token", auth.CSRF(token))
	r.AddCookie(&http.Cookie{Name: "mychef_session", Value: token})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPersonalHTTPPhotoSaveDeliveryAndRevocation(t *testing.T) {
	a, h, _, token, other, _, _, diagnostics := personalHTTPFixture(t)
	var raw bytes.Buffer
	if err := png.Encode(&raw, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	metadata := strings.Replace(personalRequestBody(t, strings.Repeat("a", 32), httpPersonalDocument(), ""), `"keep"`, `"replace"`, 1)
	body, media := photoHTTPBody(t, metadata, raw.Bytes())
	recipe := readHTTPPersonal(t, callPersonalPhoto(h, "POST", "/api/v1/personal-recipes", body, media, token, a.Config.Origin), 201)
	if recipe.PhotoID == nil || recipe.ImageURL == nil || !strings.HasPrefix(*recipe.ImageURL, "/api/v1/personal-photos/") {
		t.Fatal("photo response exposed provider URL")
	}
	for _, tc := range []struct {
		token string
		want  int
	}{{token, 200}, {other, 404}, {"", 401}} {
		w := call(h, "GET", *recipe.ImageURL, "", tc.token, "", "")
		if w.Code != tc.want {
			t.Fatal("photo ownership check failed", w.Code)
		}
		if tc.want == 200 {
			if w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("Location") != "" || bytes.Equal(w.Body.Bytes(), raw.Bytes()) {
				t.Fatal("unsafe photo delivery")
			}
		}
	}
	a.Config.PersonalPhotos = interceptedPersonalPhotos{reader: a.Config.PersonalPhotos, after: func(data []byte) []byte { data[0] ^= 1; return data }}
	if got := call(h, "GET", *recipe.ImageURL, "", token, "", ""); got.Code != 503 || bytes.HasPrefix(got.Body.Bytes(), []byte{0xff, 0xd8}) {
		t.Fatal("changed provider bytes delivered")
	}
	a.Config.PersonalPhotos = interceptedPersonalPhotos{reader: a.Config.PersonalPhotos.(interceptedPersonalPhotos).reader, after: func(data []byte) []byte {
		if err := a.Store.DB.Where("hash=?", auth.Hash(token)).Delete(&store.Session{}).Error; err != nil {
			t.Fatal(err)
		}
		return data
	}}
	if got := call(h, "GET", *recipe.ImageURL, "", token, "", ""); got.Code != 401 {
		t.Fatal("session revoked during provider read delivered photo", got.Code)
	}
	if err := a.Store.DB.Where("hash=?", auth.Hash(token)).Delete(&store.Session{}).Error; err != nil {
		t.Fatal(err)
	}
	if got := call(h, "GET", *recipe.ImageURL, "", token, "", "").Code; got != 401 {
		t.Fatal("revoked session read private photo")
	}
	if strings.Contains(diagnostics.String(), "original-private-camera-name") || strings.Contains(diagnostics.String(), "HTTP-PRIVATE-") {
		t.Fatal("private upload entered diagnostics")
	}
}

func TestPersonalHTTPQueueSwapAndDeleteRemainOwnerBound(t *testing.T) {
	a, h, owner, token, other, _, _, _ := personalHTTPFixture(t)
	request := func(method, path, body, session string) *httptest.ResponseRecorder {
		return call(h, method, path, body, session, auth.CSRF(session), a.Config.Origin)
	}
	root := "/api/v1/personal-recipes"
	recipe := readHTTPPersonal(t, request("POST", root, personalRequestBody(t, strings.Repeat("a", 32), httpPersonalDocument(), ""), token), 201)
	today := time.Now().UTC()
	monday := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	week := monday.AddDate(0, 0, 7).Format(time.DateOnly)
	queuedBody := fmt.Sprintf(`{"versionId":%q,"week":%q,"today":%q,"requestKey":%q}`, recipe.VersionID, week, today.Format(time.DateOnly), strings.Repeat("b", 32))
	path := root + "/" + recipe.ID
	queued := request("POST", path+"/queue", queuedBody, token)
	if queued.Code != 200 {
		t.Fatal("private queue failed", queued.Code)
	}
	if got := request("POST", path+"/queue", queuedBody, other).Code; got != 403 && got != 404 {
		t.Fatal("foreign queue accepted", got)
	}
	meal := store.Meal{CreatorID: owner, CreationDate: monday.Format(time.DateOnly), RecipeTitle: "Deliberately replace", ImageStatus: "unavailable"}
	if err := a.Store.DB.Create(&meal).Error; err != nil {
		t.Fatal(err)
	}
	swapBody := fmt.Sprintf(`{"versionId":%q,"week":%q,"today":%q,"targetId":%q,"requestKey":%q}`, recipe.VersionID, monday.Format(time.DateOnly), today.Format(time.DateOnly), fmt.Sprint(meal.ID), strings.Repeat("c", 32))
	swapped := request("POST", path+"/swap", swapBody, token)
	if swapped.Code != 200 {
		t.Fatal("private swap failed", swapped.Code)
	}
	var planned domain.Recipe
	if json.Unmarshal(swapped.Body.Bytes(), &planned) != nil || planned.Source == nil {
		t.Fatal("personal source omitted from planned meal")
	}
	if got := request("PUT", "/api/v1/recipes/"+planned.ID+"/favorite", "{}", token).Code; got != 200 {
		t.Fatal("linked favorite save failed", got)
	}
	a.Config.PersonalEntitlements = nil
	if got := request("POST", "/api/v1/favorites/"+planned.ID+"/queue", fmt.Sprintf(`{"week":%q,"today":%q}`, week, today.Format(time.DateOnly)), token).Code; got != 403 {
		t.Fatal("linked favorite bypassed expired Premium", got)
	}
	if got := request("POST", "/api/v1/favorites/"+planned.ID+"/swap", fmt.Sprintf(`{"week":%q,"today":%q,"targetId":%q,"requestKey":%q}`, monday.Format(time.DateOnly), today.Format(time.DateOnly), planned.ID, strings.Repeat("e", 32)), token).Code; got != 403 {
		t.Fatal("linked favorite swap bypassed expired Premium", got)
	}
	readOnly := request("GET", "/api/v1/favorite-weeks/"+week, "", token)
	if readOnly.Code != 200 || !strings.Contains(readOnly.Body.String(), `"blocked":true`) {
		t.Fatal("expired queue not marked blocked")
	}
	deleteBody := fmt.Sprintf(`{"expectedVersion":%q,"requestKey":%q}`, recipe.VersionID, strings.Repeat("d", 32))
	if got := request("DELETE", path, deleteBody, other).Code; got != 404 {
		t.Fatal("foreign delete accepted", got)
	}
	if got := request("DELETE", path, deleteBody, token).Code; got != 200 {
		t.Fatal("own delete failed", got)
	}
	if got := request("GET", path, "", token).Code; got != 404 {
		t.Fatal("deleted recipe still readable")
	}
}

type interceptedPersonalPhotos struct {
	reader PersonalPhotoReader
	after  func([]byte) []byte
}

func (p interceptedPersonalPhotos) AssetBinding() string { return p.reader.AssetBinding() }
func (p interceptedPersonalPhotos) ReadPersonalPhoto(ctx context.Context, target, digest string) ([]byte, error) {
	data, err := p.reader.ReadPersonalPhoto(ctx, target, digest)
	if err == nil {
		data = p.after(data)
	}
	return data, err
}

type unreadPersonalBody struct{ reads atomic.Int32 }

func (b *unreadPersonalBody) Read([]byte) (int, error) { b.reads.Add(1); return 0, io.EOF }
func (*unreadPersonalBody) Close() error               { return nil }

func TestPersonalHTTPUploadGateRejectsBeforeReadingOriginal(t *testing.T) {
	a, h, owner, token, _, _, _, _ := personalHTTPFixture(t)
	release := a.personalUploads.acquire(owner)
	if release == nil {
		t.Fatal("initial gate unavailable")
	}
	defer release()
	body := &unreadPersonalBody{}
	r := httptest.NewRequest("POST", "/api/v1/personal-recipes", body)
	r.Header.Set("Content-Type", "multipart/form-data; boundary=synthetic")
	r.Header.Set("Origin", a.Config.Origin)
	r.Header.Set("X-CSRF-Token", auth.CSRF(token))
	r.AddCookie(&http.Cookie{Name: "mychef_session", Value: token})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" || body.reads.Load() != 0 {
		t.Fatal("busy photo request consumed input", w.Code, body.reads.Load())
	}
	global := personalUploadGate{}
	one, two := global.acquire(1), global.acquire(2)
	if one == nil || two == nil {
		t.Fatal("two global uploads not admitted")
	}
	defer one()
	defer two()
	if third := global.acquire(3); third != nil {
		third()
		t.Fatal("third global upload admitted")
	}
}

func TestPersonalHTTPMultipartRejectsOversizedOrInvalidParts(t *testing.T) {
	a, h, _, token, _, _, _, _ := personalHTTPFixture(t)
	metadata := strings.Replace(personalRequestBody(t, strings.Repeat("a", 32), httpPersonalDocument(), ""), `"keep"`, `"replace"`, 1)
	for _, kind := range []string{"duplicate", "unknown", "large-photo", "invalid-photo", "invalid-metadata"} {
		t.Run(kind, func(t *testing.T) {
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			value := metadata
			if kind == "invalid-metadata" {
				value = strings.TrimSuffix(metadata, "}") + `,"providerId":"forged"}`
			}
			if err := form.WriteField("recipe", value); err != nil {
				t.Fatal(err)
			}
			part, err := form.CreateFormFile("photo", "private-camera-name.png")
			if err != nil {
				t.Fatal(err)
			}
			raw := []byte("not an image")
			if kind == "large-photo" {
				raw = make([]byte, providers.PersonalPhotoInputLimit+1)
			}
			if _, err := part.Write(raw); err != nil {
				t.Fatal(err)
			}
			if kind == "duplicate" {
				if err := form.WriteField("recipe", metadata); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "unknown" {
				if err := form.WriteField("privateTarget", "forged"); err != nil {
					t.Fatal(err)
				}
			}
			if err := form.Close(); err != nil {
				t.Fatal(err)
			}
			w := callPersonalPhoto(h, "POST", "/api/v1/personal-recipes", body.Bytes(), form.FormDataContentType(), token, a.Config.Origin)
			want := 400
			if kind == "large-photo" {
				want = 413
			}
			if w.Code != want {
				t.Fatal("invalid upload accepted", w.Code)
			}
		})
	}
}

type personalHTTPGenerator struct {
	actor  store.PersonalActor
	legacy bool
	err    error
}

func (g *personalHTTPGenerator) Start(context.Context, int64, string, domain.MenuInput) (domain.Operation, error) {
	g.legacy = true
	return domain.Operation{}, nil
}
func (g *personalHTTPGenerator) StartPersonal(_ context.Context, actor store.PersonalActor, _ string, _ domain.MenuInput) (domain.Operation, error) {
	g.actor = actor
	return domain.Operation{}, g.err
}
func (*personalHTTPGenerator) Status(context.Context, int64, int64) (domain.Operation, error) {
	return domain.Operation{}, nil
}
func TestPersonalHTTPGenerationPassesLiveSessionToPlanning(t *testing.T) {
	a, _, owner, token, _, _, _, _ := personalHTTPFixture(t)
	g := &personalHTTPGenerator{}
	a.Generator = g
	w := call(a.Handler(nil), "POST", "/api/v1/generations", "{}", token, auth.CSRF(token), a.Config.Origin)
	if w.Code != 202 || g.legacy || g.actor.UserID != owner || g.actor.SessionHash != auth.Hash(token) {
		t.Fatal("planner did not receive authenticated personal actor", w.Code)
	}
	g.err = store.ErrPersonalPermission
	w = call(a.Handler(nil), "POST", "/api/v1/generations", "{}", token, auth.CSRF(token), a.Config.Origin)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "premium_required") {
		t.Fatal("expired planning permission lacked actionable status", w.Code)
	}
}
