package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

type Generator interface {
	Start(context.Context, int64, string, domain.MenuInput) (domain.Operation, error)
	Status(context.Context, int64, int64) (domain.Operation, error)
}
type Config struct {
	RecipeImports        *RecipeImportService
	PersonalRecipes      PersonalRecipeSaver
	PersonalPhotos       PersonalPhotoReader
	Lifecycle            *store.LifecycleJournal
	TraceDir             string
	AccountMail          bool
	AccountReady         func(context.Context) bool
	Origin               string
	Secure               bool
	Version              string
	EmailWebhook         http.Handler
	FixtureInbox         http.Handler
	PersonalFixture      http.Handler
	PersonalEntitlements store.PersonalEntitlementReader
	Reminders            *store.ReminderSettings
	TrustedProxies       []netip.Addr
	EmailRateKey         []byte
	SignUp               *auth.SignUp
	Verification         *auth.EmailVerification
	EmailChange          *auth.EmailChange
	Recovery             *auth.Recovery
	EmailReady           func(context.Context) bool
	FeedbackTo           string
}
type API struct {
	personalUploads personalUploadGate
	Store           *store.Store
	Auth            *auth.Manager
	Generator       Generator
	Config          Config
	Logger          *slog.Logger
	mu              sync.Mutex
	limits          map[string]window
	passwordLimits  passwordLimiter
}
type window struct {
	start time.Time
	count int
}
type requestContext struct {
	Session  store.Session
	Token    string
	ID       string
	Native   bool
	Family   string
	Platform string
}
type contextKey struct{}

func New(s *store.Store, g Generator, c Config, logger *slog.Logger) *API {
	if logger == nil {
		logger = slog.Default()
	}
	return &API{Store: s, Auth: &auth.Manager{Store: s, RecoveryRateKey: c.EmailRateKey}, Generator: g, Config: c, Logger: logger, limits: map[string]window{}}
}

func (a *API) Handler(frontend http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", Health)
	mux.HandleFunc("GET /readyz", a.ready)
	if a.Config.EmailWebhook != nil {
		mux.Handle("POST /api/v1/webhooks/email", a.Config.EmailWebhook)
	}
	if a.Config.FixtureInbox != nil {
		mux.Handle("POST /api/v1/fixture/inbox", a.Config.FixtureInbox)
		mux.Handle("GET /fixture/inbox", a.Config.FixtureInbox)
		mux.Handle("POST /fixture/inbox", a.Config.FixtureInbox)
	}
	if a.Config.PersonalFixture != nil {
		mux.Handle("POST /api/v1/fixture/personal-recipes/entitlement", a.Config.PersonalFixture)
		mux.Handle("GET /api/v1/fixture/personal-recipes/entitlement", a.Config.PersonalFixture)
	}
	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, r *http.Request) {
		a.write(w, 200, map[string]string{"version": a.Config.Version, "runtime": "go"})
	})
	mux.HandleFunc("GET /api/v1/session", a.session)
	mux.HandleFunc("POST /api/v1/session", a.nativeStart)
	mux.HandleFunc("POST /api/v1/refresh", a.nativeRefresh)
	if a.Config.SignUp != nil {
		mux.HandleFunc("POST /api/v1/register", a.guard(false, a.signUpRequest))
		mux.HandleFunc("POST /api/v1/signup/inspect", a.guard(false, a.signUpInspect))
		mux.HandleFunc("POST /api/v1/signup/complete", a.guard(false, a.signUpComplete))
	} else {
		// Existing in-process fixtures use the old route until their
		// registration setup is migrated. The executable always supplies SignUp.
		mux.HandleFunc("POST /api/v1/register", a.guard(false, a.register))
	}
	mux.HandleFunc("POST /api/v1/login", a.guard(false, a.login))
	mux.HandleFunc("POST /api/v1/logout", a.guard(true, a.logout))
	if a.Config.Verification != nil {
		mux.HandleFunc("POST /api/v1/email/verification/resend", a.guard(true, a.resendEmailVerification))
		mux.HandleFunc("POST /api/v1/email/verification/confirm", a.guard(true, a.confirmEmailVerification))
	}
	if a.Config.EmailChange != nil {
		mux.HandleFunc("POST /api/v1/email/change", a.guard(true, a.startEmailChange))
		mux.HandleFunc("POST /api/v1/email/change/inspect", a.guard(false, a.inspectEmailChange))
		mux.HandleFunc("POST /api/v1/email/change/decision", a.guard(false, a.decideEmailChange))
	}
	if a.Config.Recovery != nil {
		mux.HandleFunc("POST "+requestRecoveryPath, a.recoveryRoute(true, a.requestRecovery))
		mux.HandleFunc("POST "+completeRecoveryPath, a.recoveryRoute(false, a.completeRecovery))
	}
	mux.HandleFunc("POST /api/v1/account/export", a.guard(true, a.exportAccount))
	mux.HandleFunc("POST /api/v1/account/deletion", a.guard(true, a.deleteAccount))
	a.feedbackRoutes(mux)
	a.personalRoutes(mux)
	a.recipeImportRoutes(mux)
	a.reminderRoutes(mux)
	mux.HandleFunc("GET /api/v1/profile", a.guardVerified(a.profile))
	mux.HandleFunc("PUT /api/v1/profile", a.guardVerified(a.updateProfile))
	mux.HandleFunc("PUT /api/v1/account/password", a.guardVerified(a.changePassword))
	mux.HandleFunc("POST /api/v1/generations", a.guardVerified(a.generate))
	mux.HandleFunc("GET /api/v1/generations/{id}", a.guardVerified(a.generation))
	mux.HandleFunc("GET /api/v1/menus", a.guardVerified(a.history))
	mux.HandleFunc("GET /api/v1/menus/{id}", a.guardVerified(a.menu))
	mux.HandleFunc("GET /api/v1/recipes/{id}", a.guardVerified(a.recipe))
	mux.HandleFunc("GET /api/v1/favorites", a.guardVerified(a.favorites))
	mux.HandleFunc("PUT /api/v1/recipes/{id}/favorite", a.guardVerified(a.favorite))
	mux.HandleFunc("DELETE /api/v1/recipes/{id}/favorite", a.guardVerified(a.favorite))
	mux.HandleFunc("GET /api/v1/favorite-weeks/{week}", a.guardVerified(a.favoriteWeek))
	mux.HandleFunc("POST /api/v1/favorites/{id}/queue", a.guardVerified(a.queueFavorite))
	mux.HandleFunc("POST /api/v1/favorites/{id}/swap", a.guardVerified(a.swapFavorite))
	mux.HandleFunc("GET /api/v1/shopping/{week}", a.guardVerified(a.shopping))
	mux.HandleFunc("PUT /api/v1/shopping/{week}", a.guardVerified(a.saveShopping))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		a.fail(w, r, 404, "not_found", "That resource was not found.")
	})
	if frontend != nil {
		for _, path := range []string{"/app/signup", "/app/verify-email", "/app/change-email", "/app/reset-password"} {
			mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Referrer-Policy", "no-referrer")
				shell := r.Clone(r.Context())
				shell.URL.Path, shell.URL.RawPath, shell.URL.RawQuery = "/app/", "", ""
				frontend.ServeHTTP(w, shell)
			})
		}
		mux.Handle("/", frontend)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idBytes := make([]byte, 16)
		if _, err := rand.Read(idBytes); err != nil {
			http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
			return
		}
		id := hex.EncodeToString(idBytes)
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https: data: blob:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if len(r.URL.Path) >= 4 && r.URL.Path[:4] == "/api" {
			w.Header().Set("Cache-Control", "no-store")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		r = r.WithContext(context.WithValue(ctx, contextKey{}, requestContext{ID: id}))
		defer func() {
			failure := recover()
			if err, ok := failure.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(http.ErrAbortHandler)
			}
			if failure != nil {
				a.Logger.Error("request failed", "requestId", id)
				a.fail(w, r, 500, "internal", "The request could not be completed.")
			}
		}()
		// Normalize before lifecycle lock selection, including the deletion
		// route which takes its own exclusive account-work lock.
		if strings.HasPrefix(r.URL.Path, nativePrefix) {
			suffix := strings.TrimPrefix(r.URL.Path, nativePrefix)
			if strings.HasPrefix(suffix, "fixture/") || strings.HasPrefix(suffix, "webhooks/") {
				a.fail(w, r, http.StatusNotFound, "not_found", "That resource was not found.")
				return
			}
			r = nativeRequest(r)
		}
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" && a.Config.AccountReady != nil && !a.Config.AccountReady(r.Context()) {
			a.fail(w, r, 503, "account_service_unavailable", "Account services are temporarily unavailable.")
			return
		}

		if a.Config.Lifecycle != nil && len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" && r.URL.Path != "/api/v1/account/deletion" && !strings.HasPrefix(r.URL.Path, "/api/v1/personal-") && !strings.HasPrefix(r.URL.Path, "/api/v1/recipe-imports") {
			a.Store.AccountWork.RLock()
			defer a.Store.AccountWork.RUnlock()
		}
		mux.ServeHTTP(w, r)
	})
}

func info(r *http.Request) requestContext {
	v, _ := r.Context().Value(contextKey{}).(requestContext)
	return v
}
func (a *API) write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func (a *API) fail(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	a.write(w, status, map[string]any{"error": map[string]string{"code": code, "message": message, "requestId": info(r).ID}})
}
func (a *API) err(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		a.fail(w, r, 404, "not_found", "That resource was not found.")
	case errors.Is(err, auth.ErrSession), errors.Is(err, store.ErrAccountAccess):
		a.fail(w, r, 401, "unauthenticated", "Your session has expired. Please sign in.")
	case errors.Is(err, auth.ErrCredentials):
		a.fail(w, r, 401, "unauthenticated", "The email or password is incorrect.")
	case errors.Is(err, auth.ErrConflict):
		a.fail(w, r, 409, "conflict", "Those account details are unavailable.")
	case errors.Is(err, auth.ErrPasswordLength):
		a.fail(w, r, 400, "password_length", "Choose a password of 10 to 128 characters, within 512 bytes.")
	case errors.Is(err, auth.ErrPasswordCommon):
		a.fail(w, r, 400, "password_common", "Choose a less common password.")
	case errors.Is(err, auth.ErrInput):
		a.fail(w, r, 400, "invalid_input", "Check the supplied details and try again.")
	default:
		a.Logger.Error("storage request failed", "requestId", info(r).ID)
		a.fail(w, r, 503, "storage_unavailable", "Storage is unavailable. Please try again later.")
	}
}
func (a *API) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		a.fail(w, r, 400, "invalid_input", "Send a JSON request.")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil || dec.Decode(new(any)) != io.EOF {
		a.fail(w, r, 400, "invalid_input", "Check the request fields and size.")
		return false
	}
	return true
}
func (a *API) allowed(key string, limit int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	v := a.limits[key]
	if v.start.IsZero() || now.Sub(v.start) >= time.Minute {
		v = window{start: now}
	}
	if len(a.limits) >= 10000 {
		for k, w := range a.limits {
			if now.Sub(w.start) >= time.Minute {
				delete(a.limits, k)
			}
		}
		if len(a.limits) >= 10000 && a.limits[key].start.IsZero() {
			return false
		}
	}
	if v.count >= limit {
		return false
	}
	v.count++
	a.limits[key] = v
	return true
}
func address(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
func (a *API) throttle(w http.ResponseWriter, r *http.Request, key string, limit int) bool {
	if a.allowed(key, limit) {
		return true
	}
	w.Header().Set("Retry-After", "60")
	a.fail(w, r, 429, "rate_limited", "Too many attempts. Wait a minute and try again.")
	return false
}
func (a *API) guard(required bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if info(r).Native {
			a.nativeGuard(required, next)(w, r)
			return
		}
		cookie, err := r.Cookie("mychef_session")
		if err != nil {
			a.fail(w, r, 401, "unauthenticated", "Please sign in.")
			return
		}
		session, err := a.Auth.Load(r.Context(), cookie.Value)
		if err != nil {
			if errors.Is(err, auth.ErrSession) {
				a.fail(w, r, 401, "unauthenticated", "Your session has expired. Please sign in.")
			} else {
				a.err(w, r, err)
			}
			return
		}
		if required && session.UserID == nil {
			a.fail(w, r, 401, "unauthenticated", "Please sign in.")
			return
		}
		if session.UserID != nil && a.Config.Verification == nil {
			w.Header().Set("X-MyChef-User", strconv.FormatInt(*session.UserID, 10))
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if r.Header.Get("Origin") != a.Config.Origin || !auth.ValidCSRF(cookie.Value, r.Header.Get("X-CSRF-Token")) {
				a.fail(w, r, 403, "csrf_invalid", "Refresh the page before trying again.")
				return
			}
		}
		current := info(r)
		current.Session = session
		current.Token = cookie.Value
		next(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, current)))
	}
}
func (a *API) cookie(w http.ResponseWriter, token string, authenticated bool) {
	seconds := 3600
	if authenticated {
		seconds = 7 * 24 * 3600
	}
	http.SetCookie(w, &http.Cookie{Name: "mychef_session", Value: token, Path: "/", HttpOnly: true, Secure: a.Config.Secure, SameSite: http.SameSiteLaxMode, MaxAge: seconds})
}
func (a *API) session(w http.ResponseWriter, r *http.Request) {
	if info(r).Native {
		a.nativeGuard(false, a.nativeSession)(w, r)
		return
	}
	token := ""
	if c, e := r.Cookie("mychef_session"); e == nil {
		token = c.Value
	}
	session, err := a.Auth.Load(r.Context(), token)
	if errors.Is(err, auth.ErrSession) {
		if !a.throttle(w, r, "session:"+address(r), 60) {
			return
		}
		token, err = a.Auth.Rotate(r.Context(), nil, "")
		if err == nil {
			a.cookie(w, token, false)
			session = store.Session{}
		}
	}
	if err != nil {
		a.err(w, r, err)
		return
	}
	var profile *store.Profile
	restricted := false
	if session.UserID != nil {
		var u store.User
		if a.Config.Verification != nil {
			u, err = a.liveSessionUser(r.Context(), *session.UserID, token)
		} else {
			err = a.Store.DB.WithContext(r.Context()).First(&u, *session.UserID).Error
		}
		if err != nil {
			a.err(w, r, err)
			return
		}
		restricted = a.Config.Verification != nil && u.EmailVerifiedAt == nil
		if !restricted {
			p, profileErr := a.Store.AccountProfile(r.Context(), u)
			if profileErr != nil {
				a.err(w, r, profileErr)
				return
			}
			profile = &p
		}
	}
	a.sessionResponse(w, 200, token, profile, restricted)
}
func (a *API) register(w http.ResponseWriter, r *http.Request) {
	if !a.throttle(w, r, "register:"+address(r), 5) {
		return
	}
	var body auth.Registration
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	user, err := a.Auth.Register(r.Context(), body)
	if err != nil {
		a.err(w, r, err)
		return
	}
	user, token, err := a.Auth.RegistrationSession(r.Context(), user, info(r).Token)
	if err != nil {
		a.err(w, r, err)
		return
	}
	a.signedIn(w, r, user, token, 201)
}
func (a *API) login(w http.ResponseWriter, r *http.Request) {
	if !a.throttle(w, r, "login:"+address(r), 15) {
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	_, accountKey, err := store.EmailIdentity(body.Email)
	if err != nil {
		accountKey = body.Email
	}
	if !a.throttle(w, r, "account:"+auth.Hash(accountKey), 10) {
		return
	}
	u, token, err := a.Auth.LoginSession(r.Context(), body.Email, body.Password, info(r).Token)
	if err != nil {
		a.err(w, r, err)
		return
	}
	a.signedIn(w, r, u, token, 200)
}
func (a *API) signedIn(w http.ResponseWriter, r *http.Request, u store.User, token string, status int) {
	if info(r).Native {
		a.nativeSignedIn(w, r, u, token, status)
		return
	}
	a.cookie(w, token, true)
	if a.Config.Verification != nil && u.EmailVerifiedAt == nil {
		a.sessionResponse(w, status, token, nil, true)
		return
	}
	profile, err := a.Store.AccountProfile(r.Context(), u)
	if err != nil {
		a.err(w, r, err)
		return
	}
	a.sessionResponse(w, status, token, &profile, false)
}
func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if info(r).Native {
		if err := a.Auth.NativeRevoke(r.Context(), info(r).Family); err != nil {
			a.err(w, r, err)
			return
		}
		a.write(w, http.StatusNoContent, nil)
		return
	}
	if err := a.Auth.Revoke(r.Context(), info(r).Token); err != nil {
		a.err(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "mychef_session", Value: "", Path: "/", HttpOnly: true, Secure: a.Config.Secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	a.write(w, 204, nil)
}
func (a *API) profile(w http.ResponseWriter, r *http.Request) {
	var u store.User
	if err := a.Store.DB.WithContext(r.Context()).First(&u, *info(r).Session.UserID).Error; err != nil {
		a.err(w, r, err)
		return
	}
	profile, err := a.Store.AccountProfile(r.Context(), u)
	if err != nil {
		a.err(w, r, err)
		return
	}
	a.write(w, 200, profile)
}
func (a *API) updateProfile(w http.ResponseWriter, r *http.Request) {
	var body auth.ProfileUpdate
	if !a.decode(w, r, &body) {
		return
	}
	var tokens []string
	if info(r).Native {
		tokens = []string{info(r).Token}
	}
	u, err := a.Auth.Update(r.Context(), *info(r).Session.UserID, body, tokens...)
	if err != nil {
		a.err(w, r, err)
		return
	}
	a.write(w, 200, u)
}
func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if a.Store.CheckSchema(ctx) != nil {
		a.fail(w, r, 503, "storage_unavailable", "Storage is not ready.")
		return
	}
	if a.Config.EmailReady != nil && !a.Config.EmailReady(ctx) {
		a.fail(w, r, 503, "email_unavailable", "Email is not ready.")
		return
	}
	Health(w, r)
}
func identifier(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, auth.ErrInput
	}
	return id, nil
}
func (a *API) menu(w http.ResponseWriter, r *http.Request) {
	id, err := identifier(r)
	if err != nil {
		a.err(w, r, err)
		return
	}
	v, err := a.Store.GetMenu(r.Context(), *info(r).Session.UserID, id)
	if err != nil {
		a.err(w, r, err)
		return
	}
	a.write(w, 200, v)
}
func (a *API) recipe(w http.ResponseWriter, r *http.Request) {
	id, err := identifier(r)
	if err != nil {
		a.err(w, r, err)
		return
	}
	v, err := a.Store.GetRecipe(r.Context(), *info(r).Session.UserID, id)
	if err != nil {
		a.err(w, r, err)
		return
	}
	a.write(w, 200, v)
}
func (a *API) history(w http.ResponseWriter, r *http.Request) {
	from, e1 := time.Parse(time.DateOnly, r.URL.Query().Get("from"))
	to, e2 := time.Parse(time.DateOnly, r.URL.Query().Get("to"))
	if e1 != nil || e2 != nil || !to.After(from) || to.Sub(from) > 366*24*time.Hour {
		a.err(w, r, auth.ErrInput)
		return
	}
	limit, offset := 25, 0
	var err error
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil {
			a.err(w, r, auth.ErrInput)
			return
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil {
			a.err(w, r, auth.ErrInput)
			return
		}
	}
	if limit < 1 || limit > 50 || offset < 0 || offset > 1000000 {
		a.err(w, r, auth.ErrInput)
		return
	}
	page, err := a.Store.History(r.Context(), *info(r).Session.UserID, from.Format(time.DateOnly), to.Format(time.DateOnly), limit, offset)
	if err != nil {
		a.err(w, r, err)
		return
	}
	a.write(w, 200, page)
}
func (a *API) generate(w http.ResponseWriter, r *http.Request) {
	if a.Generator == nil {
		a.fail(w, r, 503, "provider_unavailable", "Generation is not ready.")
		return
	}
	if !a.throttle(w, r, "generate:"+strconv.FormatInt(*info(r).Session.UserID, 10), 5) {
		return
	}
	var input domain.MenuInput
	if !a.decode(w, r, &input) {
		return
	}
	var op domain.Operation
	var err error
	if personal, ok := a.Generator.(PersonalGenerator); ok {
		op, err = personal.StartPersonal(r.Context(), personalActor(r), r.Header.Get("Idempotency-Key"), input)
	} else {
		op, err = a.Generator.Start(r.Context(), *info(r).Session.UserID, r.Header.Get("Idempotency-Key"), input)
	}
	if err != nil {
		a.generationError(w, r, err)
		return
	}
	a.write(w, 202, op)
}
func (a *API) generation(w http.ResponseWriter, r *http.Request) {
	if a.Generator == nil {
		a.fail(w, r, 503, "provider_unavailable", "Generation is not ready.")
		return
	}
	id, err := identifier(r)
	if err != nil {
		a.err(w, r, err)
		return
	}
	op, err := a.Generator.Status(r.Context(), *info(r).Session.UserID, id)
	if err != nil {
		a.generationError(w, r, err)
		return
	}
	a.write(w, 200, op)
}
func (a *API) generationError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrPersonalPermission) || errors.Is(err, store.ErrPersonalSession) || errors.Is(err, store.ErrPersonalNotFound) {
		a.personalError(w, r, err)
		return
	}
	if errors.Is(err, store.ErrFavoriteInput) || errors.Is(err, store.ErrFavoriteConflict) {
		a.favoriteError(w, r, err)
		return
	}
	type coded interface{ Code() string }
	var e coded
	if errors.As(err, &e) {
		switch e.Code() {
		case "menu_exists":
			a.fail(w, r, 409, e.Code(), providers.PublicMessage(e.Code()))
		case "conflict":
			a.fail(w, r, 409, e.Code(), "This request key is already used or another menu is still generating.")
		case "invalid_input":
			a.fail(w, r, 400, e.Code(), "Check the menu dates and preferences.")
		case "rate_limited":
			a.fail(w, r, 429, e.Code(), "Generation is busy. Try again later.")
		default:
			a.fail(w, r, 503, "provider_unavailable", "Generation is unavailable.")
		}
		return
	}
	a.err(w, r, err)
}
