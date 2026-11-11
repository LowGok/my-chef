package store

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func nativeLifecycleFamily(t *testing.T, s *Store, user *int64, label, platform string) NativeFamily {
	t.Helper()
	family := NativeFamily{ID: strings.Repeat(label, 32), AnchorHash: "a" + strings.Repeat(label, 63), Platform: platform, ExpiresUnix: time.Now().Add(time.Hour).Unix()}
	access := "b" + strings.Repeat(label, 63)
	for _, row := range []any{
		&Session{Hash: family.AnchorHash, UserID: user, ExpiresUnix: family.ExpiresUnix},
		&family,
		&Session{Hash: access, UserID: user, ExpiresUnix: family.ExpiresUnix},
		&NativeAccess{Hash: access, FamilyID: family.ID},
		&NativeRefresh{Hash: "c" + strings.Repeat(label, 63), FamilyID: family.ID},
	} {
		if err := s.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return family
}

func TestNativeAccountExportIncludesOnlyOwnedInstallationMetadata(t *testing.T) {
	s, actor := lifecycleExportFixture(t)
	family := nativeLifecycleFamily(t, s, &actor.UserID, "1", "ios")
	other := int64(2)
	nativeLifecycleFamily(t, s, &other, "2", "android")
	var out bytes.Buffer
	if err := s.WriteAccountExport(t.Context(), actor, &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	files := readLifecycleExport(t, out.Bytes())
	var rows []map[string]any
	if err := json.Unmarshal(files["native-installations.json"], &rows); err != nil || len(rows) != 1 || rows[0]["platform"] != "ios" {
		t.Fatal("missing or foreign native installation metadata")
	}
	for _, body := range files {
		for _, secret := range []string{family.ID, family.AnchorHash, "b" + strings.Repeat("1", 63), "c" + strings.Repeat("1", 63)} {
			if bytes.Contains(body, []byte(secret)) {
				t.Fatal("native authority leaked into account export")
			}
		}
	}
}

func TestNativeAccountDeletionCascadesOnlyItsFamilies(t *testing.T) {
	s, journal, actor := accountDeletionFixture(t)
	nativeLifecycleFamily(t, s, &actor.UserID, "1", "ios")
	otherID := int64(2)
	other := nativeLifecycleFamily(t, s, &otherID, "2", "android")
	if _, err := journal.DeleteAccount(t.Context(), actor, strings.Repeat("7", 32), "DELETE", time.Now(), AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"native_families", "native_accesses", "native_refreshes"} {
		var count int64
		if err := s.DB.Table(table).Count(&count).Error; err != nil || count != 1 {
			t.Fatal("account deletion retained native rows or erased another installation")
		}
	}
	var family NativeFamily
	if err := s.DB.Where("id=?", other.ID).Take(&family).Error; err != nil {
		t.Fatal("unrelated family removed")
	}
}

func TestNativeRestoreInvalidatesAllFamiliesWithoutChangingTheSource(t *testing.T) {
	s, journal, actor := accountDeletionFixture(t)
	nativeLifecycleFamily(t, s, &actor.UserID, "1", "ios")
	nativeLifecycleFamily(t, s, nil, "2", "android")
	backup := filepath.Join(t.TempDir(), "native-backup.db")
	if err := s.Backup(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "native-restored.db")
	if err := Restore(t.Context(), backup, destination, AccountRestoreOptions{JournalPath: journal.Path}); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(destination, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	for _, table := range []string{"native_families", "native_accesses", "native_refreshes"} {
		var count int64
		if err := restored.DB.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("restore reactivated native credentials")
		}
		if err := s.DB.Table(table).Count(&count).Error; err != nil || count != 2 {
			t.Fatal("restore changed source credentials")
		}
	}
	var retained int64
	if err := restored.DB.Table("sessions").Where("hash=?", actor.SessionHash).Count(&retained).Error; err != nil || retained != 1 {
		t.Fatal("restore unexpectedly erased the existing browser-session policy")
	}
}
