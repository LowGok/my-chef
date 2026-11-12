package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/store"
)

const nativePrefix = "/api/native/v1/"

// Only the server's route establishes the transport. Client platform/header
// metadata never changes the authority of a browser request.
func nativeRequest(r *http.Request) *http.Request {
	current := info(r)
	current.Native = true
	next := r.Clone(context.WithValue(r.Context(), contextKey{}, current))
	next.URL.Path = "/api/v1/" + strings.TrimPrefix(r.URL.Path, nativePrefix)
	next.URL.RawPath = ""
	return next
}

func nativeBearer(r *http.Request) string {
	if r.Header.Get("Cookie") != "" || len(r.Header.Values("Authorization")) != 1 {
		return ""
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func (a *API) nativeGuard(required bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := nativeBearer(r)
		current, err := a.Auth.NativeLoad(r.Context(), token)
		if err != nil {
			a.err(w, r, err)
			return
		}
		if required && current.Session.UserID == nil {
			a.fail(w, r, http.StatusUnauthorized, "unauthenticated", "Please sign in.")
			return
		}
		if current.Session.UserID != nil && a.Config.Verification == nil {
			w.Header().Set("X-MyChef-User", strconv.FormatInt(*current.Session.UserID, 10))
		}
		actor := info(r)
		actor.Session, actor.Token = current.Session, token
		actor.Family, actor.Platform = current.FamilyID, current.Platform
		next(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, actor)))
	}
}

func (a *API) nativeControl(w http.ResponseWriter, r *http.Request) bool {
	if !info(r).Native {
		a.fail(w, r, http.StatusNotFound, "not_found", "That resource was not found.")
		return false
	}
	if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
		a.err(w, r, auth.ErrSession)
		return false
	}
	return a.throttle(w, r, "native-control:"+emailClientAddress(r, a.Config.TrustedProxies), 30)
}

func (a *API) nativeStart(w http.ResponseWriter, r *http.Request) {
	if !a.nativeControl(w, r) {
		return
	}
	var body struct {
		Platform string `json:"platform"`
	}
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	tokens, err := a.Auth.NativeStart(r.Context(), body.Platform)
	if err != nil {
		a.err(w, r, err)
		return
	}
	a.nativeResponse(w, http.StatusCreated, &tokens, nil, false)
}

func (a *API) nativeRefresh(w http.ResponseWriter, r *http.Request) {
	if !a.nativeControl(w, r) {
		return
	}
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	tokens, err := a.Auth.NativeRefresh(r.Context(), body.RefreshToken)
	if err != nil {
		a.err(w, r, err)
		return
	}
	a.write(w, http.StatusOK, map[string]any{"native": tokens})
}

func (a *API) nativeResponse(w http.ResponseWriter, status int, tokens *auth.NativeTokens, profile *store.Profile, restricted bool) {
	response := map[string]any{"user": profile, "csrfToken": ""}
	if tokens != nil {
		response["native"] = *tokens
	}
	if restricted {
		response["verificationRequired"] = true
	}
	a.write(w, status, response)
}

func (a *API) nativeSession(w http.ResponseWriter, r *http.Request) {
	current := info(r)
	var profile *store.Profile
	restricted := false
	if current.Session.UserID != nil {
		user, err := a.liveSessionUser(r.Context(), *current.Session.UserID, current.Token)
		if err != nil {
			a.err(w, r, err)
			return
		}
		restricted = a.Config.Verification != nil && user.EmailVerifiedAt == nil
		if !restricted {
			p, err := a.Store.AccountProfile(r.Context(), user)
			if err != nil {
				a.err(w, r, err)
				return
			}
			profile = &p
		}
	}
	a.nativeResponse(w, http.StatusOK, nil, profile, restricted)
}

func (a *API) nativeSignedIn(w http.ResponseWriter, r *http.Request, user store.User, token string, status int) {
	restricted := a.Config.Verification != nil && user.EmailVerifiedAt == nil
	var profile *store.Profile
	if !restricted {
		p, err := a.Store.AccountProfile(r.Context(), user)
		if err != nil {
			a.err(w, r, err)
			return
		}
		profile = &p
	}
	current := info(r)
	tokens, err := a.Auth.NativeBind(r.Context(), token, current.Platform, current.Family)
	if err != nil {
		a.err(w, r, err)
		return
	}
	a.nativeResponse(w, status, &tokens, profile, restricted)
}
