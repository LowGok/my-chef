package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func recipeImageFixture(t *testing.T) (*Store, *LifecycleJournal, AccountActor) {
	t.Helper()
	s, j, actor := accountDeletionFixture(t)
	if _, err := s.SQL.ExecContext(t.Context(), "UPDATE meals SET cloudinary_public_id=NULL,image_status='pending' WHERE meal_id=27"); err != nil {
		t.Fatal(err)
	}
	return s, j, actor
}
func TestLifecycleImageIntentSurvivesLostResponseAndDeletion(t *testing.T) {
	s, j, actor := recipeImageFixture(t)
	ctx := t.Context()
	work, err := j.ReserveRecipeImage(ctx, actor.UserID, 27)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(work.Target, "mychef/") || len(work.Target) != 39 {
		t.Fatal("unstable reserved provider identity")
	}
	if err := j.BeginImageUpload(ctx, work.ID, work.Target); err != nil {
		t.Fatal(err)
	}
	if err := j.FinishImageUpload(ctx, work.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("6", 32), "DELETE", time.Now(), AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	path := j.Path
	_ = j.Close()
	again, err := s.OpenLifecycleJournal(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	pending, err := again.WorkFor(ctx, work.Incarnation)
	if err != nil || len(pending) != 1 || pending[0].Target != work.Target || pending[0].StartedNS == 0 || !pending[0].Finished || pending[0].Confirmed {
		t.Fatal("lost upload response became untracked", err)
	}
	if err := again.BeginImageUpload(ctx, work.ID, work.Target); err == nil {
		t.Fatal("deleted account started another provider side effect")
	}
}
func TestLifecycleLateConfirmedUploadCannotRestoreAccount(t *testing.T) {
	s, j, actor := recipeImageFixture(t)
	ctx := t.Context()
	work, err := j.ReserveRecipeImage(ctx, actor.UserID, 27)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.BeginImageUpload(ctx, work.ID, work.Target); err != nil {
		t.Fatal(err)
	}
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("7", 32), "DELETE", time.Now(), AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := j.FinishImageUpload(ctx, work.ID, true); err != nil {
		t.Fatal(err)
	}
	pending, err := j.WorkFor(ctx, work.Incarnation)
	if err != nil || len(pending) != 1 || !pending[0].Confirmed {
		t.Fatal("late accepted asset lost cleanup identity", err)
	}
	var n int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM users WHERE user_id=1").Scan(&n); err != nil || n != 0 {
		t.Fatal("late provider result restored account", err)
	}
}
func TestLifecycleUploadNeverStartsAfterDeletion(t *testing.T) {
	_, j, actor := recipeImageFixture(t)
	ctx := t.Context()
	work, err := j.ReserveRecipeImage(ctx, actor.UserID, 27)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("8", 32), "DELETE", time.Now(), AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := j.BeginImageUpload(ctx, work.ID, work.Target); err == nil {
		t.Fatal("provider dispatch passed deletion fence")
	}
	if err := j.FinishImageUpload(ctx, work.ID, false); err != nil {
		t.Fatal(err)
	}
	pending, err := j.WorkFor(ctx, work.Incarnation)
	if err != nil || len(pending) != 0 {
		t.Fatal("never-dispatched upload retained fake cleanup payload", err)
	}
}
func TestLifecycleTraceOwnershipAndLateWriteFence(t *testing.T) {
	s, j, actor := accountDeletionFixture(t)
	ctx := t.Context()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(root, "generation-11-1234")
	other := filepath.Join(root, "generation-22-1234")
	if _, err := s.SQL.ExecContext(ctx, `INSERT INTO generations(id,user_id,request_key,request_hash,request_json,status,stage) VALUES(22,2,'other-plan','other-hash','{}','failed','failed')`); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{owned, other} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "request.json"), []byte("private-trace-canary"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.GuardAccountTrace(ctx, actor.UserID, root, owned, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := j.GuardAccountTrace(ctx, 2, root, owned, func() error { return nil }); err == nil {
		t.Fatal("another account rewrote an owned trace")
	}
	opts := AccountDeletionOptions{Capture: func(tx *gorm.DB, u User, inc string) error { return j.CaptureAccountTraces(ctx, tx, u.ID, inc, root) }}
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("9", 32), "DELETE", time.Now(), opts); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := j.GuardAccountTrace(ctx, actor.UserID, root, owned, func() error {
		called = true
		return os.WriteFile(filepath.Join(owned, "response.json"), []byte("late-private-canary"), 0600)
	}); err == nil || called {
		t.Fatal("late trace writer passed account fence")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("another owner's trace affected", err)
	}
	var row lifecycleDeletion
	if err := j.store.DB.Where("operation=?", strings.Repeat("9", 32)).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	work, err := j.WorkFor(ctx, row.Incarnation)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range work {
		if entry.Kind == "trace" {
			if entry.Target != owned || entry.Root != root {
				t.Fatal("captured unrelated trace")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("owned trace missing durable cleanup")
	}
}

func TestLifecycleDeletionCancelsOnlyOwnedInFlightWork(t *testing.T) {
	_, j, actor := accountDeletionFixture(t)
	ctx := t.Context()
	owned, release, err := j.TrackAccountWork(ctx, actor.UserID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	other, releaseOther, err := j.TrackAccountWork(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseOther()
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("b", 32), "DELETE", time.Now(), AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	if owned.Err() == nil || other.Err() != nil {
		t.Fatal("deletion did not cancel exactly the owned provider work")
	}
	if _, release, err := j.TrackAccountWork(ctx, actor.UserID); err == nil {
		release()
		t.Fatal("deleted account scheduled more work")
	}
}
