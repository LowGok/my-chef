package httpapi

import (
	"errors"
	"net/http"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/store"
)

func (a *API) shopping(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.Shopping(r.Context(), *info(r).Session.UserID, r.PathValue("week"), personalActor(r))
	if err != nil {
		a.shoppingError(w, r, err)
		return
	}
	a.write(w, 200, list)
}
func (a *API) saveShopping(w http.ResponseWriter, r *http.Request) {
	var input domain.ShoppingList
	if !a.decode(w, r, &input) {
		return
	}
	list, err := a.Store.SaveShopping(r.Context(), *info(r).Session.UserID, r.PathValue("week"), input, personalActor(r))
	if err != nil {
		a.shoppingError(w, r, err)
		return
	}
	a.write(w, 200, list)
}
func (a *API) shoppingError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrShoppingInput):
		a.fail(w, r, 400, "invalid_input", "Check the shopping week and item details.")
	case errors.Is(err, store.ErrShoppingConflict):
		a.fail(w, r, 409, "shopping_conflict", "This list or its meals changed. Review the latest list and retry your pending changes.")
	default:
		a.err(w, r, err)
	}
}
