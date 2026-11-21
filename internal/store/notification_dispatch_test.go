package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/notifications"
)

func reminderDispatchFixture(t *testing.T, count int) (*Store, *ReminderSettings, []PersonalActor, func(time.Time)) {
	t.Helper()
	s, base, _, _, _ := personalFixture(t)
	at := time.Date(2026, 11, 16, 16, 0, 0, 0, time.UTC)
	ctx := t.Context()
	clock := func() time.Time { return at }
	reader := EntitlementReader(FixtureEntitlementLease{}, nil)
	service, err := NewReminderSettings(s, bytes.Repeat([]byte{7}, 32), reader, clock)
	if err != nil {
		t.Fatal(err)
	}
	actors := make([]PersonalActor, count)
	for i := range actors {
		session := fmt.Sprintf("dispatch-access-%d", i)
		anchor := fmt.Sprintf("dispatch-anchor-%d", i)
		family := fmt.Sprintf("%032x", i+1)
		actors[i] = PersonalActor{UserID: base.UserID, SessionHash: session, Native: true}
		for _, item := range []struct {
			sql  string
			args []any
		}{
			{"INSERT INTO sessions(hash,user_id,expires_unix) VALUES(?,?,?),(?,?,?)", []any{anchor, base.UserID, at.Add(time.Hour).Unix(), session, base.UserID, at.Add(time.Hour).Unix()}},
			{"INSERT INTO native_families VALUES(?,?,?,?)", []any{family, anchor, "android", at.Add(time.Hour).Unix()}},
			{"INSERT INTO native_accesses VALUES(?,?)", []any{session, family}},
		} {
			if err := s.DB.Exec(item.sql, item.args...).Error; err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			if err := s.DB.Exec("UPDATE users SET email_verified_at=? WHERE user_id=?", at.Unix(), base.UserID).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := service.SetPreferences(ctx, actors[i], ReminderPreferences{Zone: "Europe/Brussels", Current: true}); err != nil {
				t.Fatal(err)
			}
		}
		if i < 2 {
			if err := service.Register(ctx, actors[i], family, "android", "fcm", strings.Repeat(fmt.Sprint(i+1), 64), true); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s, service, actors, func(next time.Time) { at = next }
}

func TestReminderDispatchFreezesDevicesAndKeepsUncertainTerminal(t *testing.T) {
	s, service, actors, _ := reminderDispatchFixture(t, 3)
	ctx := t.Context()
	var first notifications.Delivery
	var seen []notifications.Delivery
	send := func(_ context.Context, d notifications.Delivery) notifications.Result {
		seen = append(seen, d)
		if len(seen) == 1 {
			first = d
			return notifications.Result{State: notifications.Uncertain}
		}
		return notifications.Result{State: notifications.Accepted}
	}
	if sent, err := service.RunReminderStep(ctx, send); err != nil || !sent {
		t.Fatal("first dispatch failed", err)
	}
	if err := service.Register(ctx, actors[2], fmt.Sprintf("%032x", 3), "android", "fcm", strings.Repeat("3", 64), true); err != nil {
		t.Fatal(err)
	}
	index := 0
	if first.Token == strings.Repeat("2", 64) {
		index = 1
	}
	if err := service.Register(ctx, actors[index], fmt.Sprintf("%032x", index+1), "android", "fcm", strings.Repeat("4", 64), true); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := service.RunReminderStep(ctx, send); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 || first.Token == seen[1].Token || seen[1].Token == strings.Repeat("3", 64) || seen[1].Token == strings.Repeat("4", 64) {
		t.Fatal("eligible fan-out repeated or reached late device", len(seen))
	}
	if got, err := service.OpenReminder(ctx, actors[0], first.Reference); err != nil || got != "2026-11-16" {
		t.Fatal("an uncertain submission can still have been received", err)
	}
	if got, err := service.OpenReminder(ctx, actors[1], seen[1].Reference); err != nil || got != "2026-11-16" {
		t.Fatal("accepted notification did not open correct week", got, err)
	}
	var count int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM reminder_occurrences").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate account occurrences", count, err)
	}
}

func TestReminderHealthDistinguishesAcceptanceFromUncertainty(t *testing.T) {
	_, service, _, _ := reminderDispatchFixture(t, 2)
	for _, result := range []notifications.State{notifications.Uncertain, notifications.Accepted} {
		sent, err := service.RunReminderStep(t.Context(), func(context.Context, notifications.Delivery) notifications.Result {
			return notifications.Result{State: result}
		})
		if err != nil || !sent {
			t.Fatal("expected fixture dispatch", err)
		}
	}
	health, err := service.ReminderHealth(t.Context())
	if err != nil || health.Accepted != 1 || health.Uncertain != 1 || health.Queued != 0 {
		t.Fatalf("unexpected safe dispatch counters: %+v, %v", health, err)
	}
}

func TestReminderDispatchRechecksOptOutAndSavedWeek(t *testing.T) {
	for _, reason := range []string{"opt-out", "meal", "registration", "deleted"} {
		t.Run(reason, func(t *testing.T) {
			s, service, actors, _ := reminderDispatchFixture(t, 1)
			ctx := t.Context()
			if err := service.evaluateReminderPreferences(ctx); err != nil {
				t.Fatal(err)
			}
			switch reason {
			case "opt-out":
				_, err := service.SetPreferences(ctx, actors[0], ReminderPreferences{Zone: "Europe/Brussels"})
				if err != nil {
					t.Fatal(err)
				}
			case "meal":
				if err := s.DB.Exec("INSERT INTO meals(creator_id,creationdate,recipetitle) VALUES(?,'2026-11-20','Saved dinner')", actors[0].UserID).Error; err != nil {
					t.Fatal(err)
				}
			case "registration":
				if err := service.Unregister(ctx, actors[0], fmt.Sprintf("%032x", 1), "android"); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := s.DB.Exec("DELETE FROM users WHERE user_id=?", actors[0].UserID).Error; err != nil {
					t.Fatal(err)
				}
			}
			if _, err := service.RunReminderStep(ctx, func(context.Context, notifications.Delivery) notifications.Result {
				t.Error("ineligible dispatch contacted provider")
				return notifications.Result{State: notifications.Accepted}
			}); err != nil && !errors.Is(err, sql.ErrNoRows) {
				t.Fatal(err)
			}
		})
	}
}

func TestReminderDispatchRecoversWithoutResendingAndCanRetryExplicitRejection(t *testing.T) {
	s, service, _, clock := reminderDispatchFixture(t, 2)
	ctx := t.Context()
	if err := service.evaluateReminderPreferences(ctx); err != nil {
		t.Fatal(err)
	}
	_, interrupted, _, err := service.claimReminder(ctx)
	if err != nil || interrupted.Reference == "" {
		t.Fatal("fixture did not mark dispatch", err)
	}
	if err := service.RecoverReminderDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	at := service.now()
	var first string
	for _, step := range []struct {
		at    time.Time
		state notifications.State
		want  bool
	}{
		{at, notifications.Temporary, true},
		{at.Add(time.Minute), notifications.Accepted, false},
		{at.Add(2 * time.Minute), notifications.Accepted, true},
		{at.Add(3 * time.Minute), notifications.Accepted, false},
	} {
		clock(step.at)
		sent, err := service.RunReminderStep(ctx, func(_ context.Context, d notifications.Delivery) notifications.Result {
			if d.Reference == interrupted.Reference {
				t.Fatal("recovered an uncertain dispatch by resending")
			}
			if first == "" {
				first = d.Reference
			}
			if first != d.Reference {
				t.Fatal("retry changed reference")
			}
			return notifications.Result{State: step.state, RetryAfter: 2 * time.Minute}
		})
		if err != nil || sent != step.want {
			t.Fatalf("at %s wanted send=%v: %v, %v", step.at, step.want, sent, err)
		}
	}
	var state string
	if err := s.SQL.QueryRow("SELECT state FROM reminder_dispatches WHERE reference=?", interrupted.Reference).Scan(&state); err != nil || state != "uncertain" {
		t.Fatal("crash did not preserve uncertainty", state, err)
	}
}
