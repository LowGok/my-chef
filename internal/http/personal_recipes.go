package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/imports"
	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
)

type PersonalRecipeSaver interface {
	Save(context.Context, store.PersonalActor, store.PersonalSave, []byte, string) (store.PersonalRecipe, error)
}
type PersonalPhotoReader interface {
	AssetBinding() string
	ReadPersonalPhoto(context.Context, string, string) ([]byte, error)
}
type PersonalGenerator interface {
	StartPersonal(context.Context, store.PersonalActor, string, domain.MenuInput) (domain.Operation, error)
}
type personalUploadGate struct {
	mu    sync.Mutex
	users map[int64]bool
}

func (g *personalUploadGate) acquire(user int64) func() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.users[user] || len(g.users) >= 2 {
		return nil
	}
	if g.users == nil {
		g.users = map[int64]bool{}
	}
	g.users[user] = true
	return func() { g.mu.Lock(); defer g.mu.Unlock(); delete(g.users, user) }
}

func personalActor(r *http.Request) store.PersonalActor {
	current := info(r)
	return store.PersonalActor{UserID: *current.Session.UserID, SessionHash: auth.Hash(current.Token)}
}
func (a *API) personalPlanning(r *http.Request) store.PersonalPlanning {
	return store.PersonalPlanning{Actor: personalActor(r), Entitlements: a.Config.PersonalEntitlements, Now: time.Now()}
}

func (a *API) guardPersonal(next http.HandlerFunc) http.HandlerFunc {
	return a.guard(true, func(w http.ResponseWriter, r *http.Request) {
		current := info(r)
		user, err := a.liveSessionUser(r.Context(), *current.Session.UserID, current.Token)
		if err != nil {
			a.err(w, r, err)
			return
		}
		if user.EmailVerifiedAt == nil || *user.EmailVerifiedAt <= 0 {
			a.fail(w, r, 403, "email_verification_required", "Verify your email to use My recipes.")
			return
		}
		w.Header().Set("X-MyChef-User", strconv.FormatInt(user.ID, 10))
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next(w, r)
	})
}

func (a *API) personalRoutes(mux *http.ServeMux) {
	for path, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/personal-recipes/permissions": a.personalPermissions,
		"POST /api/v1/personal-recipes/search":     a.searchPersonal,
		"POST /api/v1/personal-recipes/copy":       a.copyPersonal,
		"POST /api/v1/personal-recipes":            a.savePersonal,
		"GET /api/v1/personal-recipes/{id}":        a.readPersonal,
		"PUT /api/v1/personal-recipes/{id}":        a.savePersonal,
		"DELETE /api/v1/personal-recipes/{id}":     a.deletePersonal,
		"POST /api/v1/personal-recipes/{id}/queue": a.queuePersonal,
		"POST /api/v1/personal-recipes/{id}/swap":  a.swapPersonal,
		"DELETE /api/v1/personal-queue/{id}":       a.removePersonalQueue,
		"GET /api/v1/personal-photos/{id}":         a.personalPhoto,
	} {
		// Save and photo delivery fence their individual storage phases, never
		// provider I/O. Other personal handlers can hold the account fence for
		// their bounded transaction and response, just like the existing API.
		if path != "POST /api/v1/personal-recipes" && path != "PUT /api/v1/personal-recipes/{id}" && path != "GET /api/v1/personal-photos/{id}" {
			bounded := handler
			handler = func(w http.ResponseWriter, r *http.Request) {
				a.Store.AccountWork.RLock()
				defer a.Store.AccountWork.RUnlock()
				bounded(w, r)
			}
		}
		mux.HandleFunc(path, a.guardPersonal(handler))
	}
}

func (a *API) personalError(w http.ResponseWriter, r *http.Request, err error) {
	var fields *domain.PersonalRecipeValidationError
	var conflict *store.PersonalVersionConflict
	var existing *store.PersonalCopyExists
	switch {
	case errors.As(err, &fields):
		a.write(w, 400, map[string]any{"error": map[string]any{"code": "personal_validation", "message": "Check the recipe fields.", "requestId": info(r).ID, "fields": fields.Fields}})
	case errors.As(err, &conflict):
		a.write(w, 409, map[string]any{"error": map[string]any{"code": "personal_version_conflict", "message": "A newer version is saved. Review it before saving your draft.", "requestId": info(r).ID, "current": conflict.Current, "changedFields": conflict.Fields}})
	case errors.As(err, &existing):
		a.write(w, 409, map[string]any{"error": map[string]any{"code": "personal_copy_exists", "message": "This source is already in My recipes.", "requestId": info(r).ID, "existingId": existing.ID}})
	case errors.Is(err, imports.ErrImportReviewRequired), errors.Is(err, store.ErrRecipeImportNotFound), errors.Is(err, store.ErrRecipeImportConflict), errors.Is(err, store.ErrRecipeImportStorage):
		a.importError(w, r, err)
	case errors.Is(err, store.ErrPersonalSession):
		a.err(w, r, auth.ErrSession)
	case errors.Is(err, store.ErrPersonalPermission):
		a.fail(w, r, 403, "premium_required", "Premium is needed to create, edit or plan personal recipes.")
	case errors.Is(err, store.ErrPersonalNotFound), errors.Is(err, store.ErrPersonalDeleted):
		a.fail(w, r, 404, "not_found", "That recipe or photo was not found.")
	case errors.Is(err, store.ErrPersonalInput):
		a.fail(w, r, 400, "invalid_input", "Check the request fields, encoding and size.")
	case errors.Is(err, store.ErrPersonalKeyConflict):
		a.fail(w, r, 409, "personal_key_conflict", "This request changed. Review your draft before trying again.")
	case errors.Is(err, store.ErrFavoriteConflict):
		a.favoriteError(w, r, err)
	case errors.Is(err, store.ErrPersonalPhotoBusy):
		w.Header().Set("Retry-After", "2")
		a.fail(w, r, 429, "photo_busy", "Photo saves are busy. Try again shortly.")
	case errors.Is(err, store.ErrPersonalPhotoLimit):
		w.Header().Set("Retry-After", "3600")
		a.fail(w, r, 429, "photo_limit", "The photo save limit has been reached. Try again later.")
	case errors.Is(err, providers.ErrPersonalPhotoSize):
		a.fail(w, r, 413, "photo_size", "Choose a photo up to 12 MB, 8192 pixels per side and 20 million pixels.")
	case errors.Is(err, providers.ErrPersonalPhotoInvalid):
		a.fail(w, r, 400, "photo_invalid", "Choose a JPEG, PNG or static WebP photo.")
	case errors.Is(err, providers.ErrPersonalPhotoProcess):
		a.fail(w, r, 400, "photo_processing", "This photo could not be processed. Choose a smaller file and try again.")
	case errors.Is(err, store.ErrPersonalPhotoFailed):
		a.fail(w, r, 503, "photo_failed", "The photo was not saved. Retry, remove it or discard your changes.")
	case errors.Is(err, store.ErrPersonalStorage):
		a.fail(w, r, 503, "personal_unavailable", "My recipes is temporarily unavailable. Try again shortly.")
	default:
		a.err(w, r, err)
	}
}

func (a *API) decodePersonal(w http.ResponseWriter, r *http.Request, dst any, maximum int64) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		a.personalError(w, r, store.ErrPersonalInput)
		return false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maximum))
	if err != nil || json.Unmarshal(data, dst, json.RejectUnknownMembers(true)) != nil {
		a.personalError(w, r, store.ErrPersonalInput)
		return false
	}
	return true
}
func (a *API) personalNoQuery(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" {
		a.personalError(w, r, store.ErrPersonalInput)
		return false
	}
	return true
}

func (a *API) personalPermissions(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	allowed, err := a.Store.PersonalCapability(r.Context(), personalActor(r), a.Config.PersonalEntitlements, time.Now())
	if err != nil {
		a.personalError(w, r, err)
		return
	}
	a.write(w, 200, map[string]bool{"canWrite": allowed, "photoUploadsEnabled": a.Config.PersonalPhotos != nil && a.Config.PersonalRecipes != nil})
}
func (a *API) searchPersonal(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	input := struct {
		Query  string `json:"query"`
		Limit  int    `json:"limit"`
		Cursor string `json:"cursor"`
	}{Limit: 12}
	if !a.decodePersonal(w, r, &input, 4096) {
		return
	}
	page, err := a.Store.SearchPersonalRecipes(r.Context(), personalActor(r), input.Query, input.Limit, input.Cursor, time.Now())
	if err != nil {
		a.personalError(w, r, err)
		return
	}
	a.write(w, 200, page)
}
func (a *API) readPersonal(w http.ResponseWriter, r *http.Request) {
	query, queryErr := url.ParseQuery(r.URL.RawQuery)
	if queryErr != nil || len(query) > 1 || len(query) == 1 && (len(query["version"]) != 1 || query.Get("version") == "") {
		a.personalError(w, r, store.ErrPersonalInput)
		return
	}
	recipe, err := a.Store.ReadPersonalRecipe(r.Context(), personalActor(r), r.PathValue("id"), query.Get("version"), time.Now())
	if err != nil {
		a.personalError(w, r, err)
		return
	}
	a.write(w, 200, recipe)
}
func (a *API) copyPersonal(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	var input struct {
		RecipeID string `json:"recipeId"`
	}
	if !a.decodePersonal(w, r, &input, 2048) {
		return
	}
	id, err := strconv.ParseInt(input.RecipeID, 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != input.RecipeID {
		a.personalError(w, r, store.ErrPersonalInput)
		return
	}
	draft, err := a.Store.CopyPersonalRecipe(r.Context(), personalActor(r), a.Config.PersonalEntitlements, id, time.Now())
	if err != nil {
		a.personalError(w, r, err)
		return
	}
	a.write(w, 200, draft)
}

type personalSaveImportInput struct {
	OperationID  string                     `json:"operationId"`
	Confirmation imports.ReviewConfirmation `json:"confirmation"`
}

const personalSaveEnvelopeLimit = domain.PersonalRecipePayloadLimit + (64 << 10)

type personalSaveInput struct {
	Import          *personalSaveImportInput     `json:"import,omitempty"`
	RequestKey      string                       `json:"requestKey"`
	ExpectedVersion string                       `json:"expectedVersion"`
	Content         domain.PersonalRecipeContent `json:"content"`
	SourceRecipeID  *string                      `json:"sourceRecipeId,omitempty"`
	PhotoAction     string                       `json:"photoAction"`
}

func (a *API) personalSaveInput(w http.ResponseWriter, r *http.Request) (personalSaveInput, []byte, error) {
	var input personalSaveInput
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return input, nil, store.ErrPersonalInput
	}
	if media == "application/json" {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, personalSaveEnvelopeLimit))
		if err != nil || json.Unmarshal(data, &input, json.RejectUnknownMembers(true)) != nil {
			return input, nil, store.ErrPersonalInput
		}
		return input, nil, nil
	}
	if media != "multipart/form-data" {
		return input, nil, store.ErrPersonalInput
	}
	r.Body = http.MaxBytesReader(w, r.Body, 13<<20)
	reader, err := r.MultipartReader()
	if err != nil {
		return input, nil, store.ErrPersonalInput
	}
	var photo []byte
	haveRecipe, havePhoto := false, false
	for count := 0; ; count++ {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil || count >= 2 {
			clear(photo)
			return input, nil, store.ErrPersonalInput
		}
		switch part.FormName() {
		case "recipe":
			if haveRecipe {
				_ = part.Close()
				clear(photo)
				return input, nil, store.ErrPersonalInput
			}
			haveRecipe = true
			data, err := io.ReadAll(io.LimitReader(part, personalSaveEnvelopeLimit+1))
			_ = part.Close()
			if err != nil || len(data) > personalSaveEnvelopeLimit || json.Unmarshal(data, &input, json.RejectUnknownMembers(true)) != nil {
				clear(photo)
				return input, nil, store.ErrPersonalInput
			}
		case "photo":
			if havePhoto {
				_ = part.Close()
				clear(photo)
				return input, nil, store.ErrPersonalInput
			}
			havePhoto = true
			photo, err = io.ReadAll(io.LimitReader(part, providers.PersonalPhotoInputLimit+1))
			_ = part.Close()
			if err != nil || len(photo) > providers.PersonalPhotoInputLimit {
				clear(photo)
				return input, nil, providers.ErrPersonalPhotoSize
			}
		default:
			_ = part.Close()
			clear(photo)
			return input, nil, store.ErrPersonalInput
		}
	}
	if !haveRecipe || !havePhoto || input.PhotoAction != "replace" {
		clear(photo)
		return input, nil, store.ErrPersonalInput
	}
	return input, photo, nil
}

func (a *API) savePersonal(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	if a.Config.PersonalRecipes == nil {
		a.personalError(w, r, store.ErrPersonalStorage)
		return
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media == "multipart/form-data" {
		release := a.personalUploads.acquire(personalActor(r).UserID)
		if release == nil {
			a.personalError(w, r, store.ErrPersonalPhotoBusy)
			return
		}
		defer release()
	}
	input, raw, err := a.personalSaveInput(w, r)
	if err != nil {
		a.personalError(w, r, err)
		return
	}
	defer clear(raw)
	in := store.PersonalSave{ID: r.PathValue("id"), ExpectedVersion: input.ExpectedVersion, RequestKey: input.RequestKey, Content: input.Content, PhotoAction: input.PhotoAction}
	if input.Import != nil {
		in.Import = &store.PersonalImportSave{OperationID: input.Import.OperationID, Confirmation: input.Import.Confirmation, Vault: a.retainedImportVault()}
	}
	if input.SourceRecipeID != nil {
		id, err := strconv.ParseInt(*input.SourceRecipeID, 10, 64)
		if err != nil || id < 1 || strconv.FormatInt(id, 10) != *input.SourceRecipeID {
			a.personalError(w, r, store.ErrPersonalInput)
			return
		}
		in.SourceRecipeID = &id
	}
	result, err := a.Config.PersonalRecipes.Save(r.Context(), personalActor(r), in, raw, emailClientAddress(r, a.Config.TrustedProxies))
	if err != nil {
		a.personalError(w, r, err)
		return
	}
	status := 200
	if in.ID == "" {
		status = 201
	}
	a.write(w, status, result)
}

func (a *API) deletePersonal(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	var input struct {
		ExpectedVersion string `json:"expectedVersion"`
		RequestKey      string `json:"requestKey"`
	}
	if !a.decodePersonal(w, r, &input, 2048) {
		return
	}
	if err := a.Store.DeletePersonalRecipe(r.Context(), personalActor(r), r.PathValue("id"), input.ExpectedVersion, input.RequestKey, time.Now()); err != nil {
		a.personalError(w, r, err)
		return
	}
	a.write(w, 200, map[string]string{"status": "deleted"})
}
func (a *API) queuePersonal(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	var input struct {
		VersionID  string `json:"versionId"`
		Week       string `json:"week"`
		Today      string `json:"today"`
		RequestKey string `json:"requestKey"`
	}
	if !a.decodePersonal(w, r, &input, 2048) {
		return
	}
	if !validFavoriteWeek(input.Today, input.Week, true) {
		a.personalError(w, r, store.ErrPersonalInput)
		return
	}
	out, err := a.Store.QueuePersonalRecipe(r.Context(), personalActor(r), a.Config.PersonalEntitlements, r.PathValue("id"), input.VersionID, input.Week, input.RequestKey, time.Now())
	if err != nil {
		a.personalError(w, r, err)
		return
	}
	a.write(w, 200, out)
}
func (a *API) swapPersonal(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	var input struct {
		VersionID  string `json:"versionId"`
		Week       string `json:"week"`
		Today      string `json:"today"`
		TargetID   string `json:"targetId"`
		RequestKey string `json:"requestKey"`
	}
	if !a.decodePersonal(w, r, &input, 2048) {
		return
	}
	if !validFavoriteWeek(input.Today, input.Week, false) {
		a.personalError(w, r, store.ErrPersonalInput)
		return
	}
	out, err := a.Store.SwapPersonalRecipe(r.Context(), personalActor(r), a.Config.PersonalEntitlements, r.PathValue("id"), input.VersionID, domain.FavoriteSwap{Week: input.Week, Today: input.Today, TargetID: input.TargetID, RequestKey: input.RequestKey}, time.Now())
	if err != nil {
		a.personalError(w, r, err)
		return
	}
	a.write(w, 200, out)
}
func (a *API) removePersonalQueue(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	var input struct {
		RequestKey string `json:"requestKey"`
	}
	if !a.decodePersonal(w, r, &input, 2048) {
		return
	}
	out, err := a.Store.RemovePersonalQueuedRecipe(r.Context(), personalActor(r), r.PathValue("id"), input.RequestKey, time.Now())
	if err != nil {
		a.personalError(w, r, err)
		return
	}
	a.write(w, 200, out)
}

func (a *API) personalPhoto(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if !a.personalNoQuery(w, r) {
		return
	}
	if a.Config.Lifecycle == nil {
		a.personalError(w, r, store.ErrPersonalStorage)
		return
	}
	actor := personalActor(r)
	ctx, release, err := a.Config.Lifecycle.TrackAccountWork(r.Context(), actor.UserID)
	if err != nil {
		a.personalError(w, r, err)
		return
	}
	defer release()
	asset, err := a.Store.ReadPersonalPhoto(ctx, actor, r.PathValue("id"), time.Now())
	if err != nil {
		a.personalError(w, r, err)
		return
	}
	reader := a.Config.PersonalPhotos
	if reader == nil || reader.AssetBinding() != asset.Binding || asset.DeliveryType != "authenticated" {
		a.personalError(w, r, store.ErrPersonalStorage)
		return
	}
	data, err := reader.ReadPersonalPhoto(ctx, asset.Target, asset.Digest)
	if err != nil {
		a.personalError(w, r, store.ErrPersonalStorage)
		return
	}
	defer clear(data)
	if len(data) < 1 || len(data) > providers.PersonalPhotoLimit || len(data) != asset.Size {
		a.personalError(w, r, store.ErrPersonalStorage)
		return
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != asset.Digest {
		a.personalError(w, r, store.ErrPersonalStorage)
		return
	}
	a.Store.AccountWork.RLock()
	defer a.Store.AccountWork.RUnlock()
	current, err := a.Store.ReadPersonalPhoto(ctx, actor, asset.ID, time.Now())
	if err != nil {
		a.personalError(w, r, err)
		return
	}
	if current.Target != asset.Target || current.Binding != asset.Binding || current.Digest != asset.Digest || ctx.Err() != nil {
		a.personalError(w, r, store.ErrPersonalSession)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(200)
	_, _ = w.Write(data)
}
