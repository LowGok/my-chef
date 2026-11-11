package httpapi

import (
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/imports"
	"github.com/LowGok/my-chef/internal/store"
)

func (a *API) recipeImportRoutes(mux *http.ServeMux) {
	for path, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/recipe-imports/availability":       a.importAvailability,
		"POST /api/v1/recipe-imports/url":               a.startURLImport,
		"POST /api/v1/recipe-imports/photos":            a.startPhotoImport,
		"GET /api/v1/recipe-imports/{id}":               a.readImport,
		"POST /api/v1/recipe-imports/{id}/select":       a.selectImport,
		"POST /api/v1/recipe-imports/{id}/read-with-ai": a.readImportAI,
		"POST /api/v1/recipe-imports/{id}/review":       a.reviewImport,
		"POST /api/v1/recipe-imports/{id}/cancel":       a.cancelImport,
	} {
		bounded := handler
		if path != "GET /api/v1/recipe-imports/availability" && path != "POST /api/v1/recipe-imports/url" && path != "POST /api/v1/recipe-imports/photos" && path != "POST /api/v1/recipe-imports/{id}/read-with-ai" {
			handler = func(w http.ResponseWriter, r *http.Request) {
				a.Store.AccountWork.RLock()
				defer a.Store.AccountWork.RUnlock()
				bounded(w, r)
			}
		}
		mux.HandleFunc(path, a.guardPersonal(func(w http.ResponseWriter, r *http.Request) {
			if !a.personalNoQuery(w, r) {
				return
			}
			if a.Config.RecipeImports == nil {
				a.importError(w, r, imports.ErrExtractionUnavailable)
				return
			}
			handler(w, r)
		}))
	}
}
func (a *API) importError(w http.ResponseWriter, r *http.Request, err error) {
	var fields *domain.PersonalRecipeValidationError
	switch {
	case errors.As(err, &fields):
		a.personalError(w, r, err)
	case errors.Is(err, store.ErrPersonalSession):
		a.err(w, r, auth.ErrSession)
	case errors.Is(err, store.ErrPersonalPermission):
		a.fail(w, r, 403, "premium_required", "Premium is needed to import a recipe.")
	case errors.Is(err, store.ErrRecipeImportNotFound):
		a.fail(w, r, 404, "not_found", "That import was not found.")
	case errors.Is(err, store.ErrRecipeImportConflict), errors.Is(err, store.ErrRecipeImportBusy), errors.Is(err, store.ErrPersonalKeyConflict), errors.Is(err, imports.ErrImportReviewRequired):
		a.fail(w, r, 409, "import_changed", "This import has changed. Review its status or start a new attempt.")
	case errors.Is(err, imports.ErrImportInput), errors.Is(err, imports.ErrImportImage), errors.Is(err, imports.ErrImportSelectionRequired), errors.Is(err, store.ErrPersonalInput):
		a.fail(w, r, 400, "import_input", "Check the recipe source, choices and size limits.")
	default:
		a.fail(w, r, 503, "import_unavailable", "Import is unavailable right now. You can enter the recipe manually.")
	}
}
func (a *API) importAvailability(w http.ResponseWriter, r *http.Request) {
	service := a.Config.RecipeImports
	if err := service.capability(r.Context(), personalActor(r)); err != nil {
		a.importError(w, r, err)
		return
	}
	status, err := service.ready(r.Context(), false)
	if err != nil {
		a.write(w, 200, map[string]bool{"available": false, "aiAvailable": false, "fixture": false})
		return
	}
	a.write(w, 200, map[string]bool{"available": true, "aiAvailable": status.AIAvailable, "photosAvailable": status.AIAvailable && service.normalize != nil, "fixture": status.Fixture})
}
func (a *API) startURLImport(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RequestKey           string `json:"requestKey"`
		URL                  string `json:"url"`
		PersonalUseConfirmed bool   `json:"personalUseConfirmed"`
	}
	if !a.decodePersonal(w, r, &input, 8192) {
		return
	}
	if !input.PersonalUseConfirmed {
		a.importError(w, r, imports.ErrImportInput)
		return
	}
	op, err := a.Config.RecipeImports.startURL(r.Context(), personalActor(r), input.RequestKey, input.URL)
	if err != nil {
		a.importError(w, r, err)
		return
	}
	a.write(w, 202, op)
}
func (a *API) readImport(w http.ResponseWriter, r *http.Request) {
	service := a.Config.RecipeImports
	op, err := a.Store.ReadRecipeImport(r.Context(), personalActor(r), service.vault, r.PathValue("id"), time.Now().UTC())
	if err != nil {
		a.importError(w, r, err)
		return
	}
	a.write(w, 200, op)
}
func (a *API) selectImport(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CandidateID string `json:"candidateId"`
	}
	if !a.decodePersonal(w, r, &input, 2048) {
		return
	}
	service := a.Config.RecipeImports
	op, err := a.Store.SelectRecipeImport(r.Context(), personalActor(r), a.Config.PersonalEntitlements, service.vault, r.PathValue("id"), input.CandidateID, time.Now().UTC())
	if err != nil {
		a.importError(w, r, err)
		return
	}
	a.write(w, 200, op)
}
func (a *API) readImportAI(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AIConsentConfirmed bool `json:"aiConsentConfirmed"`
	}
	if !a.decodePersonal(w, r, &input, 1024) {
		return
	}
	if !input.AIConsentConfirmed {
		a.importError(w, r, imports.ErrImportInput)
		return
	}
	op, err := a.Config.RecipeImports.beginAI(r.Context(), personalActor(r), r.PathValue("id"))
	if err != nil {
		a.importError(w, r, err)
		return
	}
	a.write(w, 202, op)
}
func (a *API) reviewImport(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Content domain.PersonalRecipeContent `json:"content"`
	}
	if !a.decodePersonal(w, r, &input, domain.PersonalRecipePayloadLimit+1024) {
		return
	}
	service := a.Config.RecipeImports
	if err := service.capability(r.Context(), personalActor(r)); err != nil {
		a.importError(w, r, err)
		return
	}
	op, err := a.Store.ReadRecipeImport(r.Context(), personalActor(r), service.vault, r.PathValue("id"), time.Now().UTC())
	if err != nil {
		a.importError(w, r, err)
		return
	}
	if op.Stage != "ready" || op.Draft == nil {
		a.importError(w, r, store.ErrRecipeImportConflict)
		return
	}
	review, err := op.Draft.Review(input.Content)
	if err != nil {
		a.importError(w, r, err)
		return
	}
	a.write(w, 200, review)
}
func (a *API) cancelImport(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if !a.decodePersonal(w, r, &input, 1024) {
		return
	}
	if err := a.Config.RecipeImports.cancel(r.Context(), personalActor(r), r.PathValue("id")); err != nil {
		a.importError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type importPartReader struct {
	io.Reader
	body io.Closer
}

func (r importPartReader) Close() error { return r.body.Close() }
func (a *API) startPhotoImport(w http.ResponseWriter, r *http.Request) {
	service := a.Config.RecipeImports
	actor := personalActor(r)
	if err := service.capability(r.Context(), actor); err != nil {
		a.importError(w, r, err)
		return
	}
	release := a.personalUploads.acquire(actor.UserID)
	if release == nil {
		a.importError(w, r, store.ErrRecipeImportBusy)
		return
	}
	defer release()
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20+64<<10)
	reader, err := r.MultipartReader()
	if err != nil {
		a.importError(w, r, imports.ErrImportInput)
		return
	}
	metadata, err := reader.NextPart()
	if err != nil || metadata.FormName() != "import" {
		a.importError(w, r, imports.ErrImportInput)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(metadata, 2049))
	_ = metadata.Close()
	var input struct {
		RequestKey           string `json:"requestKey"`
		PersonalUseConfirmed bool   `json:"personalUseConfirmed"`
		AIConsentConfirmed   bool   `json:"aiConsentConfirmed"`
	}
	if err != nil || len(raw) > 2048 || json.Unmarshal(raw, &input, json.RejectUnknownMembers(true)) != nil || !input.PersonalUseConfirmed || !input.AIConsentConfirmed {
		a.importError(w, r, imports.ErrImportInput)
		return
	}
	key, keyErr := hex.DecodeString(input.RequestKey)
	if keyErr != nil || len(key) != 16 || hex.EncodeToString(key) != input.RequestKey {
		a.importError(w, r, imports.ErrImportInput)
		return
	}
	next := func() (io.Reader, error) {
		part, err := reader.NextPart()
		if err != nil {
			return nil, err
		}
		if part.FormName() != "photos" {
			_ = part.Close()
			return nil, imports.ErrImportInput
		}
		return importPartReader{Reader: part, body: r.Body}, nil
	}
	op, err := service.startPhotos(r.Context(), actor, input.RequestKey, next)
	if err != nil {
		a.importError(w, r, err)
		return
	}
	a.write(w, 202, op)
}

// Retained drafts stay exportable and saveable if the extraction worker is off.
func (a *API) retainedImportVault() *imports.DraftVault {
	if a.Config.RecipeImports != nil {
		return a.Config.RecipeImports.vault
	}
	vault, _ := imports.NewDraftVault(a.Config.EmailRateKey)
	return vault
}
