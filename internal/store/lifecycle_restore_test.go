package store

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLifecycleLostJournalCannotBeInitializedAgain(t *testing.T) {
	s, j, actor := accountDeletionFixture(t)
	ctx := t.Context()
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("a", 32), "DELETE", time.Now(), AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(j.Path); err != nil {
		t.Fatal(err)
	}
	if rebuilt, err := s.InitializeLifecycleJournal(ctx, j.Path); err == nil {
		_ = rebuilt.Close()
		t.Fatal("explicit migration recreated a lost deletion journal and its markers disappeared")
	}
	if _, err := os.Stat(j.Path); !os.IsNotExist(err) {
		t.Fatal("lost journal was silently replaced")
	}
}

func TestAccountRestoreRequiresIndependentJournal(t *testing.T) {
	s, _, _ := accountDeletionFixture(t)
	ctx := t.Context()
	backup := filepath.Join(t.TempDir(), "before.db")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	if err := Restore(ctx, backup, destination); err == nil {
		t.Fatal("restored application was published without its independent deletion journal")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("unsafe restore destination was published")
	}
}

func TestAccountRestorePurgesDeletedIncarnationAndKeepsLaterUploadCleanup(t *testing.T) {
	s, j, actor := recipeImageFixture(t)
	ctx := t.Context()
	backup := filepath.Join(t.TempDir(), "before-upload.db")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	work, err := j.ReserveRecipeImage(ctx, actor.UserID, 27, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.BeginImageUpload(ctx, work.ID, work.Target); err != nil {
		t.Fatal(err)
	}
	if err := j.FinishImageUpload(ctx, work.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("b", 32), "DELETE", time.Now(), AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	if err := Restore(ctx, backup, destination, AccountRestoreOptions{JournalPath: j.Path}); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(destination, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	for _, query := range []string{"SELECT COUNT(*) FROM users WHERE user_id=1", "SELECT COUNT(*) FROM sessions WHERE user_id=1", "SELECT COUNT(*) FROM feedback_reports WHERE user_id=1", "SELECT COUNT(*) FROM meals WHERE creator_id=1", "SELECT COUNT(*) FROM pending_email_changes WHERE user_id=1"} {
		var n int
		if err := restored.SQL.QueryRowContext(ctx, query).Scan(&n); err != nil || n != 0 {
			t.Fatal("restore reactivated deleted account data", err)
		}
	}
	var other int
	if err := restored.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE user_id=2").Scan(&other); err != nil || other != 1 {
		t.Fatal("restore purged another account", err)
	}
	paired, err := restored.OpenLifecycleJournal(ctx, j.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = paired.Close() }()
	cleaner := &fakeAssetCleaner{binding: "fixture"}
	if did, err := paired.CleanupStep(ctx, cleaner, time.Now()); err != nil || !did || len(cleaner.calls) != 1 || cleaner.calls[0] != work.Target {
		t.Fatal("backup before upload lost the independently tracked cleanup target", err)
	}
}
func TestAccountStartupReconcilesOldCopyAndPreservesFreshAccount(t *testing.T) {
	s, j, actor := recipeImageFixture(t)
	ctx := t.Context()
	snapshot := filepath.Join(t.TempDir(), "old-copy.db")
	if err := s.Backup(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("c", 32), "DELETE", time.Now(), AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	old, err := Open(snapshot, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	paired, err := old.OpenLifecycleJournal(ctx, j.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = paired.Close() }()
	if err := paired.ReconcileAccountDeletions(ctx, AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := old.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE user_id=1").Scan(&count); err != nil || count != 0 {
		t.Fatal("direct old database startup reactivated account", err)
	}
	if _, err := old.SQL.ExecContext(ctx, "INSERT INTO users(user_id,firstname,lastname,email,email_key,password,username) VALUES(1,'Fresh','Account','owner@example.test','owner@example.test','fresh','fresh')"); err != nil {
		t.Fatal(err)
	}
	if err := paired.ReconcileAccountDeletions(ctx, AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := old.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE username='fresh'").Scan(&count); err != nil || count != 1 {
		t.Fatal("old marker deleted a fresh account incarnation", err)
	}
}
func TestAccountRestoreRejectsPreIncarnationAndUncertainJournal(t *testing.T) {
	legacy := lifecycleLegacyStore(t)
	ctx := t.Context()
	destination := filepath.Join(t.TempDir(), "legacy-restore.db")
	if err := Restore(ctx, legacy.Path, destination); err == nil {
		t.Fatal("pre-incarnation snapshot received fresh identities during restore")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("legacy snapshot published before safety reconciliation")
	}
	s, j, _ := recipeImageFixture(t)
	var incarnation string
	if err := s.SQL.QueryRowContext(ctx, "SELECT incarnation FROM account_incarnations WHERE user_id=1").Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "before-prepared.db")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if err := j.PrepareDeletion(ctx, strings.Repeat("d", 32), incarnation); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "uncertain.db")
	if err := Restore(ctx, backup, target, AccountRestoreOptions{JournalPath: j.Path}); err == nil {
		t.Fatal("uncertain intent served from an old snapshot")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("uncertain restore published a destination")
	}
}

func TestLifecycleInitializationDoesNotMutateUnknownCandidate(t *testing.T) {
	s := lifecycleLegacyStore(t)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "unknown.db")
	original := []byte("not a journal: private candidate")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if j, err := s.InitializeLifecycleJournal(t.Context(), path); err == nil {
		_ = j.Close()
		t.Fatal("unknown candidate accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("unknown candidate was changed", err)
	}
}
func TestLifecycleAvailabilityDetectsReplacementAndPermissionChanges(t *testing.T) {
	_, j := lifecycleJournalFixture(t)
	if err := os.Chmod(j.Path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := j.CheckAvailable(t.Context()); err == nil {
		t.Fatal("public journal accepted")
	}
	if err := os.Chmod(j.Path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(j.Path, j.Path+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(j.Path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := j.CheckAvailable(t.Context()); err == nil {
		t.Fatal("replacement journal accepted")
	}
}

func TestAccountRestoreKeepsSharedImageCleanupTracked(t *testing.T) {
	s, j, actor := accountDeletionFixture(t)
	ctx := t.Context()
	if err := s.DB.Exec("INSERT INTO meals(meal_id,creator_id,creationdate,recipetitle,cloudinary_public_id) VALUES(77,2,'2026-11-09','Keep image','provider-secret-id')").Error; err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "shared-before.db")
	if err := s.Backup(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("d", 32), "DELETE", time.Now(), AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "shared-after.db")
	if err := Restore(ctx, snapshot, destination, AccountRestoreOptions{JournalPath: j.Path}); err != nil {
		t.Fatal(err)
	}
	var incarnation string
	if err := j.store.SQL.QueryRowContext(ctx, "SELECT incarnation FROM lifecycle_deletions WHERE operation=?", strings.Repeat("d", 32)).Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	work, err := j.WorkFor(ctx, incarnation)
	if err != nil || len(work) != 1 || work[0].Target != "provider-secret-id" {
		t.Fatal("shared image lost its durable cleanup target during deletion and restore", err)
	}
	cleaner := &fakeAssetCleaner{binding: "fixture"}
	if did, err := j.CleanupStep(ctx, cleaner, time.Now()); err != nil || !did || len(cleaner.calls) != 0 {
		t.Fatal("shared retained image was deleted", err)
	}
}
