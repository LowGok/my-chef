package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/store"
)

func (a *API) favoriteError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrFavoriteInput):
		a.fail(w, r, 400, "favorite_input", "Choose enough meal dates for your saved recipes and check the target week.")
	case errors.Is(err, store.ErrFavoriteConflict):
		a.fail(w, r, 409, "favorite_conflict", "Your saved recipes or meals changed, or the week is full. Review the latest week and try again.")
	case errors.Is(err, store.ErrPersonalPermission), errors.Is(err, store.ErrPersonalSession), errors.Is(err, store.ErrPersonalNotFound), errors.Is(err, store.ErrPersonalInput), errors.Is(err, store.ErrPersonalKeyConflict):
		a.personalError(w, r, err)
	default:
		a.err(w, r, err)
	}
}
func (a *API) favorites(w http.ResponseWriter, r *http.Request) {
	limit, offset := 12, 0
	var err error
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil {
			a.favoriteError(w, r, store.ErrFavoriteInput)
			return
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil {
			a.favoriteError(w, r, store.ErrFavoriteInput)
			return
		}
	}
	if limit < 1 || limit > 50 || offset < 0 || offset > 1000000 {
		a.favoriteError(w, r, store.ErrFavoriteInput)
		return
	}
	out, err := a.Store.Favorites(r.Context(), *info(r).Session.UserID, limit, offset)
	if err != nil {
		a.favoriteError(w, r, err)
		return
	}
	a.write(w, 200, out)
}
func (a *API) favorite(w http.ResponseWriter, r *http.Request) {
	id, err := identifier(r)
	if err != nil {
		a.err(w, r, err)
		return
	}
	out, err := a.Store.SetFavorite(r.Context(), *info(r).Session.UserID, id, r.Method == "PUT", a.personalPlanning(r))
	if err != nil {
		a.favoriteError(w, r, err)
		return
	}
	a.write(w, 200, out)
}
func (a *API) favoriteWeek(w http.ResponseWriter, r *http.Request) {
	out, err := a.Store.FavoriteWeek(r.Context(), *info(r).Session.UserID, r.PathValue("week"), a.personalPlanning(r))
	if err != nil {
		a.favoriteError(w, r, err)
		return
	}
	a.write(w, 200, out)
}

// The local calendar date may differ from UTC by one day. Never accept an arbitrary reference week.
func validFavoriteWeek(today, week string, next bool) bool {
	d, err := time.Parse(time.DateOnly, today)
	if err != nil {
		return false
	}
	utc, _ := time.Parse(time.DateOnly, time.Now().UTC().Format(time.DateOnly))
	if d.Before(utc.AddDate(0, 0, -1)) || d.After(utc.AddDate(0, 0, 1)) {
		return false
	}
	m := d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
	if next {
		m = m.AddDate(0, 0, 7)
	}
	return week == m.Format(time.DateOnly)
}
func (a *API) queueFavorite(w http.ResponseWriter, r *http.Request) {
	id, err := identifier(r)
	if err != nil {
		a.err(w, r, err)
		return
	}
	var input struct {
		Week  string `json:"week"`
		Today string `json:"today"`
	}
	if !a.decode(w, r, &input) {
		return
	}
	if !validFavoriteWeek(input.Today, input.Week, true) {
		a.favoriteError(w, r, store.ErrFavoriteInput)
		return
	}
	out, err := a.Store.QueueFavorite(r.Context(), *info(r).Session.UserID, id, input.Week, a.personalPlanning(r))
	if err != nil {
		a.favoriteError(w, r, err)
		return
	}
	a.write(w, 200, out)
}
func (a *API) swapFavorite(w http.ResponseWriter, r *http.Request) {
	id, err := identifier(r)
	if err != nil {
		a.err(w, r, err)
		return
	}
	var input domain.FavoriteSwap
	if !a.decode(w, r, &input) {
		return
	}
	if !validFavoriteWeek(input.Today, input.Week, false) {
		a.favoriteError(w, r, store.ErrFavoriteInput)
		return
	}
	out, err := a.Store.SwapFavorite(r.Context(), *info(r).Session.UserID, id, input, a.personalPlanning(r))
	if err != nil {
		a.favoriteError(w, r, err)
		return
	}
	a.write(w, 200, out)
}
