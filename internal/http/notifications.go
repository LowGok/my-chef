package httpapi

import (
	"errors"
	"net/http"

	"github.com/LowGok/my-chef/internal/store"
)

func (a *API) reminderRoutes(mux *http.ServeMux) {
	if a.Config.Reminders == nil {
		return
	}
	mux.HandleFunc("POST /api/v1/planning-reminders/open", a.guardVerified(a.reminderOpen))
	mux.HandleFunc("GET /api/v1/planning-reminders", a.guardVerified(a.reminderPreferences))
	mux.HandleFunc("PUT /api/v1/planning-reminders", a.guardVerified(a.reminderPreferences))
	mux.HandleFunc("PUT /api/v1/planning-reminders/installation", a.guardVerified(a.reminderInstallation))
	mux.HandleFunc("DELETE /api/v1/planning-reminders/installation", a.guardVerified(a.reminderInstallation))
}

func (a *API) reminderError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrReminderPermission):
		a.fail(w, r, 403, "reminder_not_allowed", "This reminder is not available for the current account or device.")
	case errors.Is(err, store.ErrReminderInput):
		a.fail(w, r, 400, "reminder_invalid", "Check the reminder settings and try again.")
	case errors.Is(err, store.ErrReminderMissing):
		a.fail(w, r, 404, "reminder_not_found", "This reminder is no longer available.")
	default:
		a.personalError(w, r, err)
	}
}

func (a *API) reminderPreferences(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	actor := personalActor(r)
	s := a.Config.Reminders
	var preferences store.ReminderPreferences
	var err error
	if r.Method == http.MethodPut {
		var input struct {
			Zone          string `json:"zone"`
			Current       bool   `json:"current"`
			Advance       bool   `json:"advance"`
			AdvanceDay    int    `json:"advanceDay"`
			AdvanceMinute int    `json:"advanceMinute"`
		}
		if !a.decodePersonal(w, r, &input, 2048) {
			return
		}
		_, err = s.SetPreferences(r.Context(), actor, store.ReminderPreferences{
			Zone: input.Zone, Current: input.Current, Advance: input.Advance,
			AdvanceDay: input.AdvanceDay, AdvanceMinute: input.AdvanceMinute,
		})
	}
	if err == nil {
		preferences, err = s.Preferences(r.Context(), actor, info(r).Family)
	}
	if err != nil {
		a.reminderError(w, r, err)
		return
	}
	a.write(w, http.StatusOK, preferences)
}

func (a *API) reminderInstallation(w http.ResponseWriter, r *http.Request) {
	actor := info(r)
	if !actor.Native {
		a.reminderError(w, r, store.ErrReminderMissing)
		return
	}
	if !a.personalNoQuery(w, r) {
		return
	}
	s := a.Config.Reminders
	var err error
	if r.Method == http.MethodDelete {
		err = s.Unregister(r.Context(), personalActor(r), actor.Family, actor.Platform)
	} else {
		var input struct {
			Token     string `json:"token"`
			OSAllowed bool   `json:"osAllowed"`
		}
		if !a.decodePersonal(w, r, &input, 2048) {
			return
		}
		provider := "fcm"
		if actor.Platform == "ios" {
			provider = "apns"
		}
		err = s.Register(r.Context(), personalActor(r), actor.Family, actor.Platform, provider, input.Token, input.OSAllowed)
	}
	if err != nil {
		a.reminderError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) reminderOpen(w http.ResponseWriter, r *http.Request) {
	if !a.personalNoQuery(w, r) {
		return
	}
	var input struct {
		Reference string `json:"reference"`
	}
	if !a.decodePersonal(w, r, &input, 1024) {
		return
	}
	week, err := a.Config.Reminders.OpenReminder(r.Context(), personalActor(r), input.Reference)
	if err != nil {
		a.reminderError(w, r, err)
		return
	}
	a.write(w, http.StatusOK, map[string]string{"week": week})
}
