package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/LowGok/my-chef/internal/analytics"
)

func (a *API) analyticsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/analytics/notice", a.analyticsNotice)
	mux.HandleFunc("GET /api/v1/analytics/preferences", a.guardVerified(a.analyticsPreference))
	mux.HandleFunc("PUT /api/v1/analytics/preferences", a.guardVerified(a.analyticsPreference))
}
func (a *API) analyticsNotice(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	notice := analytics.PublicNotice{Text: analytics.NoticeText}
	if a.Store.Analytics != nil {
		notice = a.Store.Analytics.Notice()
	}
	a.write(w, 200, notice)
}
func (a *API) analyticsPreference(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	user := *info(r).Session.UserID
	if r.Method == "GET" {
		var p analytics.Preference
		var err error
		if a.Store.Analytics != nil {
			p, err = a.Store.Analytics.Preference(r.Context(), user)
		} else {
			p, err = analytics.DisabledPreference(r.Context(), a.Store.DB, user)
		}
		if err != nil {
			a.err(w, r, err)
			return
		}
		a.write(w, 200, p)
		return
	}
	var in struct {
		Enabled          *bool   `json:"enabled"`
		NoticeVersion    *string `json:"noticeVersion"`
		ExpectedRevision *int64  `json:"expectedRevision"`
	}
	if !a.decodeCredentials(w, r, &in) {
		return
	}
	if in.Enabled == nil || in.NoticeVersion == nil || in.ExpectedRevision == nil || *in.ExpectedRevision < 0 {
		a.fail(w, r, 400, "invalid_input", "Provide enabled, noticeVersion and expectedRevision.")
		return
	}
	update := analytics.PreferenceUpdate{Enabled: *in.Enabled, NoticeVersion: *in.NoticeVersion, ExpectedRevision: *in.ExpectedRevision}
	platform := info(r).Platform
	if !info(r).Native {
		platform = "web"
	}
	var p analytics.Preference
	var err error
	if a.Store.Analytics != nil {
		p, err = a.Store.Analytics.Update(r.Context(), user, update, platform)
	} else {
		p, err = analytics.UpdateDisabledPreference(r.Context(), a.Store.DB, user, update)
	}
	switch {
	case errors.Is(err, analytics.ErrConflict):
		if a.Store.Analytics != nil {
			p, _ = a.Store.Analytics.Preference(r.Context(), user)
		} else {
			p, _ = analytics.DisabledPreference(r.Context(), a.Store.DB, user)
		}
		a.write(w, 409, map[string]any{"error": map[string]string{"code": "analytics_conflict", "message": "Your setting changed. Review the current setting and try again."}, "current": p})
	case errors.Is(err, analytics.ErrDisabled):
		a.fail(w, r, 503, "analytics_unavailable", "Analytics is unavailable. Your account remains available.")
	case err != nil:
		a.err(w, r, err)
	default:
		a.write(w, 200, p)
	}
}
func (a *API) instrument(r *http.Request, current requestContext) *http.Request {
	platform := "web"
	if current.Native {
		platform = current.Platform
	}
	ctx := analytics.WithPlatform(r.Context(), platform)
	if a.Store.Analytics != nil && current.Session.UserID != nil {
		revision, _ := strconv.ParseInt(r.Header.Get("X-MyChef-Analytics-Revision"), 10, 64)
		ctx = a.Store.Analytics.Request(ctx, *current.Session.UserID, revision, platform)
	}
	return r.WithContext(ctx)
}
