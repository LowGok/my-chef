package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func (a *API) feedbackRoutes(mux *http.ServeMux) {
	guard := func(path string, next http.HandlerFunc) {
		if a.Config.Verification == nil {
			mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
				a.feedbackError(w, r, store.ErrFeedbackUnavailable)
			})
			return
		}
		mux.HandleFunc(path, a.guardVerified(next))
	}
	guard("GET /api/v1/feedback/availability", a.feedbackAvailability)
	guard("POST /api/v1/feedback", a.submitFeedback)
	guard("POST /api/v1/feedback/receipt", a.lookupFeedbackReceipt)
	guard("GET /api/v1/recipes/{id}/rating", a.readFeedbackRating)
	guard("PUT /api/v1/recipes/{id}/rating", a.writeFeedbackRating)
}

func feedbackActor(r *http.Request) store.FeedbackActor {
	session := info(r)
	return store.FeedbackActor{UserID: *session.Session.UserID, SessionHash: auth.Hash(session.Token)}
}

func (a *API) feedbackAvailability(w http.ResponseWriter, _ *http.Request) {
	a.write(w, http.StatusOK, map[string]bool{"enabled": a.Config.FeedbackTo != ""})
}

func (a *API) submitFeedback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RequestKey string  `json:"requestKey"`
		Topic      string  `json:"topic"`
		Message    string  `json:"message"`
		RecipeID   *string `json:"recipeId"`
	}
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	in := store.FeedbackInput{RequestKey: body.RequestKey, Topic: body.Topic, Message: body.Message}
	if body.RecipeID != nil {
		id, err := strconv.ParseInt(*body.RecipeID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != *body.RecipeID {
			a.feedbackError(w, r, store.ErrFeedbackInput)
			return
		}
		in.RecipeID = &id
	}
	now := time.Now()
	var enqueue store.FeedbackEnqueue
	if recipient := a.Config.FeedbackTo; recipient != "" {
		enqueue = func(tx *gorm.DB, notice store.FeedbackNotice) (int64, error) {
			return mailqueue.Enqueue(r.Context(), a.Store, tx, now, mailqueue.FeedbackRequest(recipient, notice))
		}
	}
	receipt, err := a.Store.WriteFeedback(r.Context(), feedbackActor(r), in, now, nil, enqueue)
	a.writeFeedbackReceipt(w, r, receipt, err)
}

func (a *API) lookupFeedbackReceipt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Operation  string `json:"operation"`
		RequestKey string `json:"requestKey"`
	}
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	receipt, err := a.Store.FeedbackReceipt(r.Context(), feedbackActor(r), body.Operation, body.RequestKey, time.Now())
	a.writeFeedbackReceipt(w, r, receipt, err)
}

func (a *API) readFeedbackRating(w http.ResponseWriter, r *http.Request) {
	id, err := identifier(r)
	if err != nil {
		a.feedbackError(w, r, store.ErrFeedbackInput)
		return
	}
	rating, err := a.Store.FeedbackRating(r.Context(), feedbackActor(r), id, time.Now())
	if err != nil {
		a.feedbackError(w, r, err)
		return
	}
	a.write(w, http.StatusOK, map[string]*int{"rating": rating})
}

func (a *API) writeFeedbackRating(w http.ResponseWriter, r *http.Request) {
	id, err := identifier(r)
	if err != nil {
		a.feedbackError(w, r, store.ErrFeedbackInput)
		return
	}
	var body struct {
		RequestKey string `json:"requestKey"`
		Rating     int    `json:"rating"`
	}
	if !a.decodeCredentials(w, r, &body) {
		return
	}
	in := store.FeedbackRatingInput{RequestKey: body.RequestKey, RecipeID: id, Rating: body.Rating}
	receipt, err := a.Store.WriteFeedbackRating(r.Context(), feedbackActor(r), in, time.Now())
	a.writeFeedbackReceipt(w, r, receipt, err)
}

func (a *API) writeFeedbackReceipt(w http.ResponseWriter, r *http.Request, receipt store.FeedbackReceipt, err error) {
	if err != nil {
		a.feedbackError(w, r, err)
		return
	}
	a.write(w, http.StatusOK, map[string]string{
		"reference": receipt.Reference,
		"createdAt": time.Unix(receipt.CreatedUnix, 0).UTC().Format(time.RFC3339),
	})
}

func (a *API) feedbackError(w http.ResponseWriter, r *http.Request, err error) {
	var limit *store.FeedbackLimit
	switch {
	case errors.Is(err, store.ErrFeedbackInput):
		a.fail(w, r, 400, "invalid_input", "Check the contact reason and the information you entered.")
	case errors.Is(err, store.ErrFeedbackSession):
		a.err(w, r, auth.ErrSession)
	case errors.Is(err, store.ErrFeedbackNotFound):
		a.fail(w, r, 404, "not_found", "That resource is unavailable.")
	case errors.Is(err, store.ErrFeedbackConflict):
		a.fail(w, r, 409, "conflict", "That request was already used for different content.")
	case errors.Is(err, store.ErrFeedbackUnchanged):
		a.fail(w, r, 409, "rating_unchanged", "This rating is already saved.")
	case errors.As(err, &limit):
		w.Header().Set("Retry-After", strconv.Itoa(limit.RetryAfter))
		a.fail(w, r, 429, "rate_limited", "Too many recent submissions. Wait and try again.")
	case errors.Is(err, store.ErrFeedbackUnavailable):
		a.fail(w, r, 503, "feedback_unavailable", "Feedback is currently unavailable. For help, open Profile Help & support.")
	default:
		a.err(w, r, err)
	}
}
