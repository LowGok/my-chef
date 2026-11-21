package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/notifications"
)

func TestReminderTapUsesThatCopySubmissionDeadline(t *testing.T) {
	s, scheduler, actors, clock := reminderDispatchFixture(t, 2)
	start := scheduler.now()
	zone := time.FixedZone("old rules", 60*60)
	scheduler.loadLocation = func(string) (*time.Location, error) { return zone, nil }
	expiry := start.Add(48 * time.Hour).Unix()
	for _, q := range []string{"UPDATE native_families SET expires_unix=?", "UPDATE sessions SET expires_unix=?"} {
		if err := s.DB.Exec(q, expiry).Error; err != nil {
			t.Fatal(err)
		}
	}
	var deliveries []notifications.Delivery
	send := func(_ context.Context, d notifications.Delivery) notifications.Result {
		deliveries = append(deliveries, d)
		return notifications.Result{State: notifications.Accepted}
	}
	if sent, err := scheduler.RunReminderStep(t.Context(), send); err != nil || !sent {
		t.Fatalf("first dispatch: %v, %v", sent, err)
	}
	zone = time.FixedZone("updated rules", 2*60*60)
	if sent, err := scheduler.RunReminderStep(t.Context(), send); err != nil || !sent {
		t.Fatalf("second dispatch: %v, %v", sent, err)
	}
	if len(deliveries) != 2 || deliveries[0].Deadline.Sub(deliveries[1].Deadline) != time.Hour {
		t.Fatal("fixture did not submit two different deadlines")
	}
	clock(deliveries[1].Deadline.Add(-time.Second))
	if _, err := scheduler.OpenReminder(t.Context(), actors[0], deliveries[1].Reference); err != nil {
		t.Fatalf("second reference rejected before its expiry: %v", err)
	}
	clock(deliveries[1].Deadline)
	if _, err := scheduler.OpenReminder(t.Context(), actors[0], deliveries[0].Reference); err != nil {
		t.Fatalf("first reference rejected before its own fixed submission expiry: %v", err)
	}
	if week, err := scheduler.OpenReminder(t.Context(), actors[0], deliveries[1].Reference); !errors.Is(err, ErrReminderMissing) {
		t.Fatalf("opened second reference for %q at %s after its own fixed submission expiry %s: %v", week, scheduler.now(), deliveries[1].Deadline, err)
	}
}

func TestReminderRechecksDeferredCopyOnSecondWindowMove(t *testing.T) {
	s, scheduler, _, clock := reminderDispatchFixture(t, 2)
	start := scheduler.now()
	zone := time.FixedZone("first rules", 60*60)
	scheduler.loadLocation = func(string) (*time.Location, error) { return zone, nil }
	expiry := start.Add(48 * time.Hour).Unix()
	for _, q := range []string{"UPDATE native_families SET expires_unix=?", "UPDATE sessions SET expires_unix=?"} {
		if err := s.DB.Exec(q, expiry).Error; err != nil {
			t.Fatal(err)
		}
	}
	refs := map[string]bool{}
	send := func(_ context.Context, d notifications.Delivery) notifications.Result {
		if refs[d.Reference] {
			t.Fatal("resent accepted reference")
		}
		refs[d.Reference] = true
		return notifications.Result{State: notifications.Accepted}
	}
	if sent, err := scheduler.RunReminderStep(t.Context(), send); err != nil || !sent {
		t.Fatalf("first copy: %t %v", sent, err)
	}
	zone = time.FixedZone("second rules", -10*60*60)
	late := notifications.CurrentWeek(scheduler.now(), zone).Scheduled
	if sent, err := scheduler.RunReminderStep(t.Context(), send); err != nil || sent {
		t.Fatalf("copy should defer: %t %v", sent, err)
	}
	zone = time.FixedZone("third rules", 14*60*60)
	clock(start.Add(time.Minute))
	current := notifications.CurrentWeek(scheduler.now(), zone)
	if !current.Due(scheduler.now()) || late.Before(current.Deadline) {
		t.Fatal("second move should allow a copy before the old hold opens")
	}
	if _, err := scheduler.RunReminderStep(t.Context(), send); err != nil {
		t.Fatal(err)
	}
	clock(late)
	if _, err := scheduler.RunReminderStep(t.Context(), send); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("only %d copies; second never sent after the schedule moved into the eligible window", len(refs))
	}
}
