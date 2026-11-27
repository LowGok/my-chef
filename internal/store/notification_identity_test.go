package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/notifications"
)

func TestReminderIdentityCannotStealSiblingTokenOrRetargetRetry(t *testing.T) {
	s, service, actors, advance := reminderDispatchFixture(t, 2)
	ctx := t.Context()
	if _, err := s.SetNativeIdentityPolicy(ctx, 0, true, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	family := fmt.Sprintf("%032x", 2)
	if err := s.DB.Exec("DELETE FROM reminder_installations WHERE family_id=?", family).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("UPDATE native_families SET identity_lane='app',identity_revision=1 WHERE id=?", family).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.Register(ctx, actors[1], family, "android", "fcm", strings.Repeat("2", 64), true); !errors.Is(err, ErrReminderPermission) {
		t.Fatal("candidate used an unconfigured provider route", err)
	}
	if err := service.SetIdentityProviderRoute("app", "production", func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if err := service.Register(ctx, actors[1], family, "android", "fcm", strings.Repeat("1", 64), true); !errors.Is(err, ErrReminderPermission) {
		t.Fatal("candidate stole known Preview token", err)
	}
	var count int
	if err := s.SQL.QueryRow("SELECT count(*) FROM reminder_installations WHERE family_id=?", fmt.Sprintf("%032x", 1)).Scan(&count); err != nil || count != 1 {
		t.Fatal("cross-lane claim disturbed original", err)
	}
	if err := service.Register(ctx, actors[1], family, "android", "fcm", strings.Repeat("2", 64), true); err != nil {
		t.Fatal(err)
	}
	var deliveries []notifications.Delivery
	for range 2 {
		_, err := service.RunReminderStep(ctx, func(ctx context.Context, d notifications.Delivery) notifications.Result {
			release, allowed := notifications.BeginSubmission(ctx)
			defer release()
			if !allowed {
				return notifications.Result{State: notifications.Permanent}
			}
			deliveries = append(deliveries, d)
			return notifications.Result{State: notifications.Temporary}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(deliveries) != 2 {
		t.Fatal("both independent opt-ins did not receive a delivery")
	}
	for _, d := range deliveries {
		want := "preview"
		if d.Token == strings.Repeat("2", 64) {
			want = "app"
		}
		if d.IdentityLane != want || d.Environment != "production" {
			t.Fatal("delivery lost immutable identity or environment")
		}
	}
	advance(time.Date(2026, 11, 16, 16, 2, 0, 0, time.UTC))
	first := append([]notifications.Delivery(nil), deliveries...)
	deliveries = nil
	for range 2 {
		if _, err := service.RunReminderStep(ctx, func(ctx context.Context, d notifications.Delivery) notifications.Result {
			release, allowed := notifications.BeginSubmission(ctx)
			defer release()
			if !allowed {
				return notifications.Result{State: notifications.Permanent}
			}
			deliveries = append(deliveries, d)
			return notifications.Result{State: notifications.Accepted}
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(deliveries) != 2 {
		t.Fatal("route-bound retries missing")
	}
	for i, d := range deliveries {
		if d.Reference != first[i].Reference || d.IdentityLane != first[i].IdentityLane || d.Environment != first[i].Environment {
			t.Fatal("retry changed its original route")
		}
	}
	for _, d := range deliveries {
		if d.IdentityLane == "app" {
			if _, err := service.OpenReminder(ctx, actors[0], d.Reference); !errors.Is(err, ErrReminderMissing) {
				t.Fatal("candidate reminder tap opened through Preview", err)
			}
			if week, err := service.OpenReminder(ctx, actors[1], d.Reference); err != nil || week != "2026-11-16" {
				t.Fatal("matching candidate cannot open its reminder", err)
			}
		}
	}
	// A changed registration cannot adopt a previously queued retry from another lane.
	if err := s.DB.Exec("UPDATE reminder_installations SET identity_lane='preview' WHERE family_id=?", family).Error; err == nil {
		t.Fatal("registration identity can be rewritten")
	}
}

func TestReminderIdentityUpgradePreservesLegacyCipherAndQueue(t *testing.T) {
	s := nativeIdentityPriorStore(t)
	if err := s.migrateNativeIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateNativeIdentityPolicy(t.Context()); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := s.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=(SELECT user_id FROM sessions WHERE hash='retained-session')").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	minimisationSQL(t, s, "INSERT INTO reminder_preferences VALUES(?, 'Europe/Brussels',1,0,0,0,1,0)", owner)
	minimisationSQL(t, s, "INSERT INTO reminder_installations(family_id,incarnation,platform,provider,token_tag,token_cipher,refreshed_unix) VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?,'ios','apns',randomblob(32),randomblob(100),100)", owner)
	minimisationSQL(t, s, "INSERT INTO reminder_occurrences VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',?,'current','2026-11-16',100,200,1,1,0)", owner)
	minimisationSQL(t, s, "INSERT INTO reminder_dispatches(reference,occurrence,family_id,state,next_attempt,attempts) VALUES('cccccccccccccccccccccccccccccccc','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','pending',100,1)")
	var before []byte
	if err := s.SQL.QueryRow("SELECT token_cipher FROM reminder_installations").Scan(&before); err != nil {
		t.Fatal(err)
	}
	minimisationSQL(t, s, "CREATE TRIGGER reject_notification_identity BEFORE UPDATE ON schema_migrations WHEN NEW.version=31 BEGIN SELECT RAISE(ABORT,'notification identity fixture'); END")
	if err := s.Migrate(t.Context()); err == nil {
		t.Fatal("injected notification migration failure ignored")
	}
	var version, partial int
	if err := s.SQL.QueryRow("SELECT version FROM schema_migrations").Scan(&version); err != nil || version != 30 {
		t.Fatal("migration failure changed prior version", err)
	}
	if err := s.SQL.QueryRow("SELECT count(*) FROM pragma_table_info('reminder_installations') WHERE name='identity_lane'").Scan(&partial); err != nil || partial != 0 {
		t.Fatal("partial installation metadata survived", err)
	}
	minimisationSQL(t, s, "DROP TRIGGER reject_notification_identity")
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"reminder_installations", "reminder_dispatches"} {
		var lane, environment string
		var revision int64
		if err := s.SQL.QueryRow("SELECT identity_lane,environment,identity_revision FROM "+table).Scan(&lane, &environment, &revision); err != nil || lane != "preview" || environment != "production" || revision != 0 {
			t.Fatal("existing work adopted new app authority", err)
		}
	}
	var after []byte
	if err := s.SQL.QueryRow("SELECT token_cipher FROM reminder_installations").Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatal("upgrade changed existing protected token", err)
	}
}
