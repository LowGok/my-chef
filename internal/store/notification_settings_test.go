package store

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReminderSettingsEnforceTierAndProtectInstallationToken(t *testing.T) {
	s, actor, other, _, now := personalFixture(t)
	ctx := t.Context()
	owner := entitlementIdentity(t, s, actor.UserID)
	if err := s.DB.Exec("UPDATE users SET email='reminder@example.test',email_key='reminder@example.test' WHERE user_id=?", actor.UserID).Error; err != nil {
		t.Fatal(err)
	}
	reader, revoke := testEntitlementSwitch(t, s, actor, owner, fixedEntitlementClock(now))
	service, err := NewReminderSettings(s, bytes.Repeat([]byte{9}, 32), reader, fixedEntitlementClock(now))
	if err != nil {
		t.Fatal(err)
	}
	family := strings.Repeat("a", 32)
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM native_families").Scan(new(int)); err != nil {
		t.Fatal(err)
	}
	for _, query := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO sessions(hash,user_id,expires_unix) VALUES(?,?,?)", []any{"reminder-native-anchor", actor.UserID, now.Add(time.Hour).Unix()}},
		{"INSERT INTO native_families(id,anchor_hash,platform,expires_unix) VALUES(?,?,?,?)", []any{family, "reminder-native-anchor", "android", now.Add(time.Hour).Unix()}},
		{"INSERT INTO native_accesses(hash,family_id) VALUES(?,?)", []any{actor.SessionHash, family}},
	} {
		if err := s.DB.Exec(query.sql, query.args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	if got, err := service.Preferences(ctx, actor); err != nil || got.Current || got.Advance || !got.Premium {
		t.Fatalf("default settings or entitlement wrong: %+v, %v", got, err)
	}
	if err := service.Register(ctx, actor, family, "android", "fcm", strings.Repeat("test-token", 12), true); !errors.Is(err, ErrReminderPermission) {
		t.Fatal("registration preceded product opt-in", err)
	}
	input := ReminderPreferences{Zone: "Europe/Brussels", Current: true, Advance: true, AdvanceDay: int(time.Sunday), AdvanceMinute: 17 * 60}
	if got, err := service.SetPreferences(ctx, actor, input); err != nil || !got.Current || !got.Advance || got.Revision < 1 {
		t.Fatalf("valid preferences failed: %+v, %v", got, err)
	}
	token := strings.Repeat("test-token", 12)
	if err := service.Register(ctx, actor, family, "android", "fcm", token, false); !errors.Is(err, ErrReminderPermission) {
		t.Fatal("registration accepted without OS permission", err)
	}
	if err := service.Register(ctx, other, family, "android", "fcm", token, true); !errors.Is(err, ErrReminderPermission) {
		t.Fatal("another account bound a native installation", err)
	}
	if err := service.Register(ctx, actor, family, "android", "fcm", token, true); err != nil {
		t.Fatal(err)
	}
	var cipher []byte
	if err := s.SQL.QueryRow("SELECT token_cipher FROM reminder_installations WHERE family_id=?", family).Scan(&cipher); err != nil || bytes.Contains(cipher, []byte(token)) {
		t.Fatal("token was not encrypted", err)
	}
	revoke()
	if got, err := service.Preferences(ctx, actor); err != nil || got.Premium || got.Advance || !got.Current {
		t.Fatalf("downgrade did not suppress advance: %+v, %v", got, err)
	}
	if _, err := service.SetPreferences(ctx, actor, input); !errors.Is(err, ErrReminderPermission) {
		t.Fatal("Free could enable advance", err)
	}
	if err := s.DB.Exec("DELETE FROM native_families WHERE id=?", family).Error; err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM reminder_installations").Scan(&count); err != nil || count != 0 {
		t.Fatal("logout left a registration", count, err)
	}
}

func TestReminderExportAndRestoreDoNotCarryRegistration(t *testing.T) {
	s, j, actor := accountDeletionFixture(t)
	ctx := t.Context()
	now := time.Now().UTC()
	if err := s.DB.Exec("UPDATE users SET email_verified_at=? WHERE user_id=?", now.Unix(), actor.UserID).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewReminderSettings(s, bytes.Repeat([]byte{6}, 32), EntitlementReader(FixtureEntitlementLease{}, nil), fixedEntitlementClock(now))
	if err != nil {
		t.Fatal(err)
	}
	session := PersonalActor{UserID: actor.UserID, SessionHash: actor.SessionHash}
	if _, err := service.SetPreferences(ctx, session, ReminderPreferences{Zone: "Europe/Brussels", Current: true}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := s.WriteAccountExport(ctx, actor, &output, now); err != nil {
		t.Fatal(err)
	}
	data := readLifecycleExport(t, output.Bytes())
	if !bytes.Contains(data["weekly-reminders.json"], []byte("Europe/Brussels")) ||
		bytes.Contains(data["weekly-reminders.json"], []byte(entitlementIdentity(t, s, actor.UserID))) {
		t.Fatal("export omitted safe preferences or disclosed internal identity")
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	dst := filepath.Join(t.TempDir(), "restored.db")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if err := Restore(ctx, backup, dst, AccountRestoreOptions{JournalPath: j.Path}); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(dst, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	var current, advance bool
	var zone string
	if err := restored.SQL.QueryRow("SELECT current_enabled,advance_enabled,zone FROM reminder_preferences").Scan(&current, &advance, &zone); err != nil || current || advance || zone != "Europe/Brussels" {
		t.Fatal("restore resumed sending or erased zone preference", err)
	}
}
