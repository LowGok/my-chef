package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/notifications"
)

func candidateReminderFixture(t *testing.T) (*Store, *ReminderSettings) {
	t.Helper()
	s, service, actors, _ := reminderDispatchFixture(t, 1)
	family := fmt.Sprintf("%032x", 1)
	if _, err := s.SetNativeIdentityPolicy(t.Context(), 0, true, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("DELETE FROM reminder_installations WHERE family_id=?", family).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("UPDATE native_families SET identity_lane='app',identity_revision=1 WHERE id=?", family).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.SetIdentityProviderRoute("app", "production", func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if err := service.Register(t.Context(), actors[0], family, "android", "fcm", strings.Repeat("1", 64), true); err != nil {
		t.Fatal(err)
	}
	return s, service
}

func TestReminderCutoffCancelsClaimBeforeSubmission(t *testing.T) {
	s, service := candidateReminderFixture(t)
	if err := service.evaluateReminderPreferences(t.Context()); err != nil {
		t.Fatal(err)
	}
	delivery, dispatch, tag, err := service.claimReminder(t.Context())
	if err != nil || dispatch.Reference == "" {
		t.Fatal("candidate not claimed", err)
	}
	if _, err := s.SetNativeIdentityPolicy(t.Context(), 1, false, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	wire := 0
	_, err = service.submitClaimedReminder(t.Context(), delivery, dispatch, tag, func(ctx context.Context, _ notifications.Delivery) notifications.Result {
		release, allowed := notifications.BeginSubmission(ctx)
		defer release()
		if !allowed {
			return notifications.Result{State: notifications.Permanent}
		}
		wire++
		return notifications.Result{State: notifications.Accepted}
	})
	if err != nil || wire != 0 {
		t.Fatal("claimed candidate crossed acknowledged cutoff", err)
	}
	var state string
	if err := s.SQL.QueryRow("SELECT state FROM reminder_dispatches WHERE reference=?", dispatch.Reference).Scan(&state); err != nil || state != "suppressed" {
		t.Fatal("unsubmitted claim not retired", state, err)
	}
}

func TestReminderCutoffKeepsSubmittedOutcomeWithoutRetry(t *testing.T) {
	for _, result := range []notifications.State{notifications.Accepted, notifications.Temporary, notifications.Uncertain} {
		t.Run(fmt.Sprint(result), func(t *testing.T) {
			s, service := candidateReminderFixture(t)
			submitted, reply := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			wire := 0
			go func() {
				_, err := service.RunReminderStep(t.Context(), func(ctx context.Context, _ notifications.Delivery) notifications.Result {
					release, allowed := notifications.BeginSubmission(ctx)
					if !allowed {
						release()
						return notifications.Result{State: notifications.Permanent}
					}
					wire++
					release()
					close(submitted)
					<-reply
					return notifications.Result{State: result}
				})
				done <- err
			}()
			select {
			case <-submitted:
			case <-time.After(3 * time.Second):
				t.Fatal("provider submission not reached")
			}
			_, err := s.SetNativeIdentityPolicy(t.Context(), 1, false, 0, time.Now())
			close(reply)
			if err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			var state string
			if err := s.SQL.QueryRow("SELECT state FROM reminder_dispatches").Scan(&state); err != nil {
				t.Fatal(err)
			}
			want := map[notifications.State]string{notifications.Accepted: "accepted", notifications.Temporary: "suppressed", notifications.Uncertain: "uncertain"}[result]
			if state != want {
				t.Fatalf("late result became %s, want %s", state, want)
			}
			if _, err := s.SetNativeIdentityPolicy(t.Context(), 2, true, 0, time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := service.RunReminderStep(t.Context(), func(context.Context, notifications.Delivery) notifications.Result {
				wire++
				return notifications.Result{State: notifications.Accepted}
			}); err != nil || wire != 1 {
				t.Fatal("re-enable replayed a closed submission", err)
			}
		})
	}
}
