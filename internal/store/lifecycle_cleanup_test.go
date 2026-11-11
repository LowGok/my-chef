package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

type fakeAssetCleaner struct {
	missing   bool
	binding   string
	calls     []string
	fail      bool
	permanent bool
}

func (f *fakeAssetCleaner) AssetBinding() string { return f.binding }
func (f *fakeAssetCleaner) DeleteImage(_ context.Context, target string) (AssetDeletionResult, error) {
	f.calls = append(f.calls, target)
	if f.fail {
		return AssetDeletionResult{Permanent: f.permanent}, errors.New("synthetic rejection")
	}
	return AssetDeletionResult{Deleted: !f.missing, Missing: f.missing}, nil
}
func cleanupImageFixture(t *testing.T, confirmed bool, binding string) (*Store, *LifecycleJournal, LifecycleWork) {
	t.Helper()
	s, j, actor := recipeImageFixture(t)
	ctx := t.Context()
	work, err := j.ReserveRecipeImage(ctx, actor.UserID, 27, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.BeginImageUpload(ctx, work.ID, work.Target); err != nil {
		t.Fatal(err)
	}
	if err := j.FinishImageUpload(ctx, work.ID, confirmed); err != nil {
		t.Fatal(err)
	}
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("d", 32), "DELETE", time.Now(), AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	return s, j, work
}
func TestLifecycleCleanupConfirmedTargetAndPayloadErasure(t *testing.T) {
	_, j, work := cleanupImageFixture(t, true, "fixture")
	ctx := t.Context()
	cleaner := &fakeAssetCleaner{binding: "fixture"}
	if did, err := j.CleanupStep(ctx, cleaner, time.Now()); err != nil || !did {
		t.Fatal(err)
	}
	if len(cleaner.calls) != 1 || cleaner.calls[0] != work.Target {
		t.Fatal("cleanup targeted another asset")
	}
	pending, err := j.WorkFor(ctx, work.Incarnation)
	if err != nil || len(pending) != 0 {
		t.Fatal("completed cleanup retained target", err)
	}
	data, err := os.ReadFile(j.Path)
	if err != nil || bytes.Contains(data, []byte(work.Target)) {
		t.Fatal("erased target remains in protected store pages", err)
	}
}
func TestLifecycleCleanupRetryAndTerminalPrivacy(t *testing.T) {
	_, j, work := cleanupImageFixture(t, true, "fixture")
	ctx := t.Context()
	now := time.Now()
	cleaner := &fakeAssetCleaner{binding: "fixture", fail: true}
	if did, err := j.CleanupStep(ctx, cleaner, now); err != nil || !did {
		t.Fatal(err)
	}
	if did, err := j.CleanupStep(ctx, cleaner, now.Add(time.Second)); err != nil || did || len(cleaner.calls) != 1 {
		t.Fatal("cleanup ignored retry backoff", err)
	}
	cleaner.fail = false
	if did, err := j.CleanupStep(ctx, cleaner, now.Add(time.Minute)); err != nil || !did {
		t.Fatal(err)
	}
	if work, err := j.WorkFor(ctx, work.Incarnation); err != nil || len(work) != 0 {
		t.Fatal("retry did not erase completed payload", err)
	}
	_, terminal, original := cleanupImageFixture(t, true, "fixture")
	rejected := &fakeAssetCleaner{binding: "fixture", fail: true, permanent: true}
	if did, err := terminal.CleanupStep(ctx, rejected, now); err != nil || !did {
		t.Fatal(err)
	}
	status, err := terminal.CleanupStatus(ctx, 100, "")
	if err != nil || len(status) != 1 || status[0].State != "review" {
		t.Fatal("missing terminal review", err)
	}
	encoded, err := json.Marshal(status)
	if err != nil || bytes.Contains(encoded, []byte(original.Target)) || bytes.Contains(encoded, []byte(original.Incarnation)) || bytes.Contains(encoded, []byte(strings.TrimPrefix(original.Target, "mychef/"))) {
		t.Fatal("routine lookup exposed protected cleanup identifiers", err)
	}
	if did, err := terminal.CleanupStep(ctx, rejected, now.Add(24*time.Hour)); err != nil || did || len(rejected.calls) != 1 {
		t.Fatal("terminal rejection retried automatically", err)
	}
}
func TestLifecycleCleanupRetainsUncertainUploadAndOriginalProviderBinding(t *testing.T) {
	ctx := t.Context()
	_, j, work := cleanupImageFixture(t, false, "cloudinary/original")
	wrong := &fakeAssetCleaner{binding: "cloudinary/replacement"}
	if did, err := j.CleanupStep(ctx, wrong, time.Now()); err != nil || !did || len(wrong.calls) != 0 {
		t.Fatal("changed credentials targeted another provider account", err)
	}
	pending, err := j.WorkFor(ctx, work.Incarnation)
	if err != nil || len(pending) != 1 || pending[0].Provider != "cloudinary/original" {
		t.Fatal("provider identity lost", err)
	}
	_, uncertain, original := cleanupImageFixture(t, false, "fixture")
	cleaner := &fakeAssetCleaner{binding: "fixture"}
	if did, err := uncertain.CleanupStep(ctx, cleaner, time.Now()); err != nil || !did {
		t.Fatal(err)
	}
	pending, err = uncertain.WorkFor(ctx, original.Incarnation)
	if err != nil || len(pending) != 1 || pending[0].Code != "upload_outcome_unconfirmed" {
		t.Fatal("unconfirmed upload was falsely completed", err)
	}
}
func TestLifecycleCleanupPreservesAnotherAccountsRetainedAsset(t *testing.T) {
	s, j, work := cleanupImageFixture(t, true, "fixture")
	ctx := t.Context()
	now := time.Now()
	cleaner := &fakeAssetCleaner{binding: "fixture"}
	if _, err := s.SQL.ExecContext(ctx, "INSERT INTO meals(meal_id,creator_id,creationdate,recipetitle,cloudinary_public_id) VALUES(77,2,'2026-11-09','Keep image',?)", work.Target); err != nil {
		t.Fatal(err)
	}
	if did, err := j.CleanupStep(ctx, cleaner, now); err != nil || !did || len(cleaner.calls) != 0 {
		t.Fatal("deleted asset referenced by another account", err)
	}
	if _, err := s.SQL.ExecContext(ctx, "DELETE FROM meals WHERE meal_id=77"); err != nil {
		t.Fatal(err)
	}
	if did, err := j.CleanupStep(ctx, cleaner, now.Add(25*time.Hour)); err != nil || !did || len(cleaner.calls) != 1 {
		t.Fatal("cleanup did not resume after retained reference disappeared", err)
	}
}

func TestLifecycleTraceCleanupDoesNotFollowSymlinksOrBlockAccountDeletion(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "symlink"}[unsafe], func(t *testing.T) {
			_, j, actor := recipeImageFixture(t)
			ctx := t.Context()
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "generation-11-1234")
			if err := j.GuardAccountTrace(ctx, actor.UserID, root, target, func() error {
				if err := os.Mkdir(target, 0700); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(target, "request.json"), []byte("owned-private-trace"), 0600)
			}); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "keep-private.txt")
			if err := os.WriteFile(outside, []byte("unrelated-private-file"), 0600); err != nil {
				t.Fatal(err)
			}
			if unsafe {
				if err := os.Remove(filepath.Join(target, "request.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(target, "request.json")); err != nil {
					t.Fatal(err)
				}
			}
			opts := AccountDeletionOptions{Capture: func(tx *gorm.DB, u User, inc string) error { return j.CaptureAccountTraces(ctx, tx, u.ID, inc, root) }}
			if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("e", 32), "DELETE", time.Now(), opts); err != nil {
				t.Fatal("filesystem cleanup condition blocked live account deletion", err)
			}
			if did, err := j.CleanupStep(ctx, nil, time.Now()); err != nil || !did {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(outside); err != nil || string(data) != "unrelated-private-file" {
				t.Fatal("followed an external symlink", err)
			}
			if unsafe {
				status, err := j.CleanupStatus(ctx, 100, "")
				if err != nil || len(status) != 1 || status[0].Code != "trace_target_unavailable" {
					t.Fatal("unsafe trace target was not held for review", err)
				}
			} else if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("owned trace remained after completed cleanup", err)
			}
		})
	}
}

func TestLifecycleCleanupLateConfirmationAfterEarlyAbsence(t *testing.T) {
	_, j, actor := recipeImageFixture(t)
	ctx := t.Context()
	now := time.Now()
	work, err := j.ReserveRecipeImage(ctx, actor.UserID, 27, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.BeginImageUpload(ctx, work.ID, work.Target); err != nil {
		t.Fatal(err)
	}
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("f", 32), "DELETE", now, AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	cleaner := &fakeAssetCleaner{binding: "fixture", missing: true}
	if did, err := j.CleanupStep(ctx, cleaner, now); err != nil || !did {
		t.Fatal(err)
	}
	if pending, err := j.WorkFor(ctx, work.Incarnation); err != nil || len(pending) != 1 {
		t.Fatal("early provider response discarded in-flight identity", err)
	}
	cleaner.missing = false
	if err := j.FinishImageUpload(ctx, work.ID, true); err != nil {
		t.Fatal(err)
	}
	if did, err := j.CleanupStep(ctx, cleaner, now.Add(time.Second)); err != nil || !did {
		t.Fatal("late confirmation did not resume cleanup", err)
	}
	if pending, err := j.WorkFor(ctx, work.Incarnation); err != nil || len(pending) != 0 {
		t.Fatal("confirmed completed cleanup retained payload", err)
	}
}
func TestLifecycleCleanupAttemptAndTimeBudgets(t *testing.T) {
	for _, byTime := range []bool{false, true} {
		t.Run(map[bool]string{false: "attempts", true: "horizon"}[byTime], func(t *testing.T) {
			_, j, _ := cleanupImageFixture(t, true, "fixture")
			ctx := t.Context()
			now := time.Now()
			cleaner := &fakeAssetCleaner{binding: "fixture", fail: true}
			if did, err := j.CleanupStep(ctx, cleaner, now); err != nil || !did {
				t.Fatal(err)
			}
			if byTime {
				if _, err := j.CleanupStep(ctx, cleaner, now.Add(24*time.Hour)); err != nil {
					t.Fatal(err)
				}
			} else {
				for n := 1; n < 16; n++ {
					if _, err := j.CleanupStep(ctx, cleaner, now.Add(time.Duration(n)*31*time.Minute)); err != nil {
						t.Fatal(err)
					}
				}
			}
			status, err := j.CleanupStatus(ctx, 100, "")
			if err != nil || len(status) != 1 || status[0].State != "review" || status[0].Code != "cleanup_exhausted" {
				t.Fatal("unbounded cleanup", err)
			}
			if byTime && len(cleaner.calls) != 1 || !byTime && len(cleaner.calls) != 16 {
				t.Fatal("incorrect cleanup attempt count")
			}
		})
	}
}
