package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/notifications"
	"gorm.io/gorm"
)

func TestReminderRegressionUnchangedSaveKeepsRemainingDevice(t *testing.T) {
	_, service, actors, _ := reminderDispatchFixture(t, 2)
	ctx := t.Context()
	calls := 0
	send := func(context.Context, notifications.Delivery) notifications.Result {
		calls++
		return notifications.Result{State: notifications.Accepted}
	}
	if sent, err := service.RunReminderStep(ctx, send); !sent || err != nil {
		t.Fatalf("initial dispatch: %v %v", sent, err)
	}
	if _, err := service.SetPreferences(ctx, actors[0], ReminderPreferences{Zone: "Europe/Brussels", Current: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunReminderStep(ctx, send); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("unchanged settings suppressed a still-eligible device: got %d sends, want 2", calls)
	}
}

func TestReminderDeviceOptOutCannotRejoinFrozenOccurrence(t *testing.T) {
	_, service, actors, _ := reminderDispatchFixture(t, 2)
	ctx := t.Context()
	send := func(context.Context, notifications.Delivery) notifications.Result {
		return notifications.Result{State: notifications.Accepted}
	}
	if sent, err := service.RunReminderStep(ctx, send); !sent || err != nil {
		t.Fatalf("initial dispatch: %v %v", sent, err)
	}
	family := fmt.Sprintf("%032x", 2)
	if err := service.Unregister(ctx, actors[1], family, "android"); err != nil {
		t.Fatal(err)
	}
	if err := service.Register(ctx, actors[1], family, "android", "fcm", strings.Repeat("2", 64), true); err != nil {
		t.Fatal(err)
	}
	if sent, err := service.RunReminderStep(ctx, send); sent || err != nil {
		t.Fatalf("opted-out device rejoined a frozen occurrence: %v %v", sent, err)
	}
}

func TestReminderConcurrentWorkersClaimEachDeviceOnce(t *testing.T) {
	_, service, _, _ := reminderDispatchFixture(t, 2)
	ctx := t.Context()
	var lock sync.Mutex
	seen := map[string]int{}
	var work sync.WaitGroup
	for range 8 {
		work.Go(func() {
			_, err := service.RunReminderStep(ctx, func(_ context.Context, delivery notifications.Delivery) notifications.Result {
				lock.Lock()
				seen[delivery.Token]++
				lock.Unlock()
				return notifications.Result{State: notifications.Uncertain}
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	work.Wait()
	if len(seen) != 2 || seen[strings.Repeat("1", 64)] != 1 || seen[strings.Repeat("2", 64)] != 1 {
		t.Fatal("concurrent workers did not claim exactly once per device")
	}
}

func TestReminderDispatchDoesNotQueueEmail(t *testing.T) {
	s, service, _, _ := reminderDispatchFixture(t, 1)
	var before, after int
	if err := s.SQL.QueryRow("SELECT count(*) FROM email_outbox").Scan(&before); err != nil {
		t.Fatal(err)
	}
	calls := 0
	sent, err := service.RunReminderStep(t.Context(), func(context.Context, notifications.Delivery) notifications.Result {
		calls++
		return notifications.Result{State: notifications.Accepted}
	})
	if err != nil || !sent || calls != 1 {
		t.Fatalf("expected one synthetic push: %v %v %d", sent, err, calls)
	}
	if err := s.SQL.QueryRow("SELECT count(*) FROM email_outbox").Scan(&after); err != nil || before != after {
		t.Fatalf("reminder wrote to the email outbox: %v", err)
	}
}

func TestReminderRegressionFreezeBeginsAtOwnFirstDispatch(t *testing.T) {
	s, service, _, _ := reminderDispatchFixture(t, 1)
	ctx := t.Context()
	addDevice := func(i int, zone string) {
		access := fmt.Sprintf("review-access-%d", i)
		anchor := fmt.Sprintf("review-anchor-%d", i)
		family := fmt.Sprintf("%032x", i)
		until := service.now().Add(time.Hour).Unix()
		actor := PersonalActor{UserID: 2, SessionHash: access, Native: true}
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{"INSERT INTO sessions(hash,user_id,expires_unix) VALUES(?,?,?),(?,?,?)", []any{anchor, 2, until, access, 2, until}},
			{"INSERT INTO native_families VALUES(?,?,?,?)", []any{family, anchor, "android", until}},
			{"INSERT INTO native_accesses VALUES(?,?)", []any{access, family}},
		} {
			if err := s.DB.Exec(q.sql, q.args...).Error; err != nil {
				t.Fatal(err)
			}
		}
		if zone != "" {
			if _, err := service.SetPreferences(ctx, actor, ReminderPreferences{Zone: zone, Current: true}); err != nil {
				t.Fatal(err)
			}
		}
		if err := service.Register(ctx, actor, family, "android", "fcm", strings.Repeat(fmt.Sprint(i), 64), true); err != nil {
			t.Fatal(err)
		}
	}
	addDevice(2, "Europe/Brussels")
	if err := service.evaluateReminderPreferences(ctx); err != nil {
		t.Fatal(err)
	}
	// Distinct due times make the first account deterministic without relying
	// on the randomly generated occurrence IDs.
	if err := s.DB.Exec(`UPDATE reminder_occurrences SET scheduled=scheduled-1
WHERE incarnation=(SELECT incarnation FROM account_incarnations WHERE user_id=1)`).Error; err != nil {
		t.Fatal(err)
	}
	var seen []string
	send := func(_ context.Context, d notifications.Delivery) notifications.Result {
		seen = append(seen, d.Token[:1])
		return notifications.Result{State: notifications.Accepted}
	}
	if sent, err := service.RunReminderStep(ctx, send); !sent || err != nil || seen[0] != "1" {
		t.Fatalf("first account dispatch: %v %v %v", seen, sent, err)
	}
	addDevice(3, "")
	for range 3 {
		if _, err := service.RunReminderStep(ctx, send); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("second account excluded a device opted in before its first dispatch: %v", seen)
	}
}

func TestReminderRegressionInactiveAdvanceDoesNotStarveCurrent(t *testing.T) {
	s, service, actors, clock := reminderDispatchFixture(t, 1)
	ctx := t.Context()
	t0 := service.now()
	clock(t0.Add(time.Minute))
	// Thirty-two earlier, ineligible advance rows have active devices. Each row belongs to a different account.
	for i := 0; i < 32; i++ {
		id := 100 + i
		owner := fmt.Sprintf("%032x", id+900)
		family := fmt.Sprintf("%032x", id)
		anchor := fmt.Sprintf("review-anchor-%d", id)
		until := t0.Add(time.Hour).Unix()
		if err := s.DB.Exec("INSERT INTO users(user_id,firstname,lastname,email,password,username,email_verified_at,email_key) VALUES(?,'Review','Fixture',?,'fake',?,?,?)", id, fmt.Sprintf("review-%d@example.test", id), fmt.Sprintf("review%d", id), t0.Unix(), fmt.Sprintf("review-%d@example.test", id)).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=?", id).Scan(&owner); err != nil {
			t.Fatal(err)
		}
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{"INSERT INTO sessions(hash,user_id,expires_unix) VALUES(?,?,?)", []any{anchor, id, until}},
			{"INSERT INTO native_families VALUES(?,?,?,?)", []any{family, anchor, "android", until}},
			{"INSERT INTO reminder_preferences VALUES(?, 'Europe/Brussels', 0, 1, 1, 1019, 1, ?)", []any{owner, until}},
			{"INSERT INTO reminder_installations VALUES(?,?,'android','fcm',randomblob(32),randomblob(100),?)", []any{family, owner, t0.Unix()}},
			{"INSERT INTO reminder_occurrences(id,incarnation,purpose,target_week,scheduled,deadline,revision,frozen) VALUES(?,?,'advance','2026-11-23',?,?,1,0)", []any{fmt.Sprintf("%032x", id+1000), owner, t0.Add(-time.Minute).Unix(), until}},
		} {
			if err := s.DB.Exec(q.sql, q.args...).Error; err != nil {
				t.Fatalf("%s: %v", q.sql, err)
			}
		}
	}
	service.read = func(_ context.Context, _ *gorm.DB, _ int64, _ string) (PersonalEntitlement, error) {
		return PersonalEntitlement{}, nil
	}
	if _, err := service.SetPreferences(ctx, actors[0], ReminderPreferences{Zone: "Europe/Brussels", Current: true}); err != nil {
		t.Fatal(err)
	}
	sent, err := service.RunReminderStep(ctx, func(context.Context, notifications.Delivery) notifications.Result {
		return notifications.Result{State: notifications.Accepted}
	})
	if !sent || err != nil {
		t.Fatalf("ineligible earlier advance rows blocked an eligible current nudge: %v %v", sent, err)
	}
}

func TestReminderRegressionFreeCannotConfigureAdvance(t *testing.T) {
	_, service, actors, _ := reminderDispatchFixture(t, 1)
	ctx := t.Context()
	input := ReminderPreferences{Zone: "Europe/Brussels", Current: true, Advance: false, AdvanceDay: 5, AdvanceMinute: 915}
	if _, err := service.SetPreferences(ctx, actors[0], input); err != nil {
		return
	}
	got, err := service.Preferences(ctx, actors[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.AdvanceDay == input.AdvanceDay && got.AdvanceMinute == input.AdvanceMinute {
		t.Fatalf("Free stored advance controls: day=%d minute=%d", got.AdvanceDay, got.AdvanceMinute)
	}
}

func TestReminderRegressionInvalidZoneMustNotBlockOtherUsers(t *testing.T) {
	s, service, actors, _ := reminderDispatchFixture(t, 1)
	ctx := t.Context()
	if err := s.DB.Exec("INSERT INTO reminder_preferences VALUES((SELECT incarnation FROM account_incarnations WHERE user_id=2), 'Legacy/Unavailable', 1, 0, 0, 0, 1, 0)").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetPreferences(ctx, actors[0], ReminderPreferences{Zone: "Europe/Brussels", Current: true}); err != nil {
		t.Fatal(err)
	}
	sent, err := service.RunReminderStep(ctx, func(context.Context, notifications.Delivery) notifications.Result {
		return notifications.Result{State: notifications.Accepted}
	})
	if !sent || err != nil {
		t.Fatalf("an unavailable persisted timezone blocked an unrelated due nudge: %v %v", sent, err)
	}
	health, err := service.ReminderHealth(ctx)
	if err != nil || health.ZoneFailures != 1 {
		t.Fatalf("unavailable zone not counted: %+v %v", health, err)
	}
}

func TestReminderRegressionSchedulingReplacesUnsentZoneWithoutDuplicate(t *testing.T) {
	s, service, actors, clock := reminderDispatchFixture(t, 1)
	ctx := t.Context()
	start := service.now()
	if err := service.evaluateReminderPreferences(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetPreferences(ctx, actors[0], ReminderPreferences{Zone: "UTC", Current: true}); err != nil {
		t.Fatal(err)
	}
	if err := service.evaluateReminderPreferences(ctx); err != nil {
		t.Fatal(err)
	}
	if _, d, _, err := service.claimReminder(ctx); d.Reference != "" || err != nil {
		t.Fatalf("old zone still authorized: %q %v", d.Reference, err)
	}
	clock(start.Add(time.Hour))
	if err := service.evaluateReminderPreferences(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	var scheduled, deadline int64
	if err := s.SQL.QueryRow("SELECT count(*),min(scheduled),min(deadline) FROM reminder_occurrences WHERE purpose='current'").Scan(&count, &scheduled, &deadline); err != nil {
		t.Fatal(err)
	}
	if count != 1 || scheduled != start.Add(time.Hour).Unix() || deadline != start.Add(25*time.Hour).Unix() {
		t.Fatalf("schedule or duplicate error: %d %d %d", count, scheduled, deadline)
	}
}

func TestReminderRegressionAdvanceDoesNotConsumeFollowingCurrent(t *testing.T) {
	s, service, actors, clock := reminderDispatchFixture(t, 1)
	ctx := t.Context()
	at := service.now()
	service.read = func(_ context.Context, _ *gorm.DB, _ int64, _ string) (PersonalEntitlement, error) {
		return PersonalEntitlement{Premium: true, ExpiresUnix: at.Add(30 * 24 * time.Hour).Unix()}, nil
	}
	for _, q := range []string{"UPDATE native_families SET expires_unix=expires_unix+1209600", "UPDATE sessions SET expires_unix=expires_unix+1209600"} {
		if err := s.DB.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.SetPreferences(ctx, actors[0], ReminderPreferences{Zone: "Europe/Brussels", Current: true, Advance: true, AdvanceDay: 1, AdvanceMinute: 17 * 60}); err != nil {
		t.Fatal(err)
	}
	var seen []notifications.Delivery
	send := func(_ context.Context, d notifications.Delivery) notifications.Result {
		seen = append(seen, d)
		return notifications.Result{State: notifications.Accepted}
	}
	for range 2 {
		if sent, err := service.RunReminderStep(ctx, send); !sent || err != nil {
			t.Fatalf("initial send: %v %v", sent, err)
		}
	}
	clock(at.Add(7 * 24 * time.Hour))
	if sent, err := service.RunReminderStep(ctx, send); !sent || err != nil {
		t.Fatalf("next week send: %v %v", sent, err)
	}
	var count int
	if err := s.SQL.QueryRow("SELECT count(*) FROM reminder_occurrences WHERE target_week='2026-11-23' AND purpose IN ('current','advance')").Scan(&count); err != nil || count != 2 {
		t.Fatalf("current and advance were not independent: %d %v", count, err)
	}
}

func TestReminderRegressionFreeDowngradeBlocksAlreadyQueuedAdvance(t *testing.T) {
	s, service, actors, _ := reminderDispatchFixture(t, 1)
	ctx := t.Context()
	at := service.now()
	premium := true
	service.read = func(_ context.Context, _ *gorm.DB, _ int64, _ string) (PersonalEntitlement, error) {
		return PersonalEntitlement{Premium: premium, ExpiresUnix: at.Add(time.Hour).Unix()}, nil
	}
	if _, err := service.SetPreferences(ctx, actors[0], ReminderPreferences{Zone: "Europe/Brussels", Current: false, Advance: true, AdvanceDay: 1, AdvanceMinute: 17 * 60}); err != nil {
		t.Fatal(err)
	}
	if err := service.evaluateReminderPreferences(ctx); err != nil {
		t.Fatal(err)
	}
	premium = false
	sent, err := service.RunReminderStep(ctx, func(context.Context, notifications.Delivery) notifications.Result {
		t.Error("downgraded advance reached adapter")
		return notifications.Result{State: notifications.Accepted}
	})
	if sent || err != nil {
		t.Fatalf("downgraded advance was not suppressed: %v %v", sent, err)
	}
	if got, err := service.Preferences(ctx, actors[0]); err != nil || got.Advance {
		t.Fatalf("advance preference exposed: %+v %v", got, err)
	}
	var count int
	if err := s.SQL.QueryRow("SELECT count(*) FROM reminder_occurrences WHERE purpose='advance'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected an already queued advance: %v %v", count, err)
	}
}

func TestReminderRegressionOpenIsOwnedReadOnlyAndExpires(t *testing.T) {
	s, service, actors, clock := reminderDispatchFixture(t, 1)
	ctx := t.Context()
	at := service.now()
	var delivery notifications.Delivery
	if sent, err := service.RunReminderStep(ctx, func(_ context.Context, d notifications.Delivery) notifications.Result {
		delivery = d
		return notifications.Result{State: notifications.Accepted}
	}); !sent || err != nil {
		t.Fatalf("send: %v %v", sent, err)
	}
	if err := s.DB.Exec("INSERT INTO meals(creator_id,creationdate,recipetitle) VALUES(?,'2026-11-20','Saved dinner')", actors[0].UserID).Error; err != nil {
		t.Fatal(err)
	}
	if target, err := service.OpenReminder(ctx, actors[0], delivery.Reference); err != nil || target != "2026-11-16" {
		t.Fatalf("saved week open failed: %q %v", target, err)
	}
	other := PersonalActor{UserID: 2, SessionHash: "personal-session-2"}
	if err := s.DB.Exec("UPDATE users SET email_verified_at=? WHERE user_id=2", at.Unix()).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("UPDATE sessions SET expires_unix=? WHERE hash=?", at.Add(time.Hour).Unix(), other.SessionHash).Error; err != nil {
		t.Fatal(err)
	}
	if target, err := service.OpenReminder(ctx, other, delivery.Reference); err != ErrReminderMissing || target != "" {
		t.Fatalf("foreign account: %q %v", target, err)
	}
	if target, err := service.OpenReminder(ctx, actors[0], strings.Repeat("1", 32)); err != ErrReminderMissing || target != "" {
		t.Fatalf("forged reference: %q %v", target, err)
	}
	var count int
	if err := s.SQL.QueryRow("SELECT count(*) FROM meals WHERE creator_id=?", actors[0].UserID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("open mutated the saved meal: %d %v", count, err)
	}
	if err := s.DB.Exec("UPDATE sessions SET expires_unix=expires_unix+172800").Error; err != nil {
		t.Fatal(err)
	}
	clock(at.Add(24 * time.Hour))
	if target, err := service.OpenReminder(ctx, actors[0], delivery.Reference); err != ErrReminderMissing || target != "" {
		t.Fatalf("expired: %q %v", target, err)
	}
}
