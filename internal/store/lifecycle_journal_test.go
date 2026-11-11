package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lifecycleJournalFixture(t *testing.T) (*Store, *LifecycleJournal) {
	t.Helper()
	s := lifecycleLegacyStore(t)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	j, err := s.InitializeLifecycleJournal(t.Context(), filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return s, j
}

func TestLifecycleJournalBindingAndMissingStore(t *testing.T) {
	s, j := lifecycleJournalFixture(t)
	ctx := t.Context()
	path := j.Path
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("private journal permissions", err)
	}
	if _, err := s.OpenLifecycleJournal(ctx, path+"-missing"); err == nil {
		t.Fatal("missing journal silently created")
	}
	if _, err := os.Stat(path + "-missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing-store read mutated disk")
	}
	second, err := s.OpenLifecycleJournal(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
	other := lifecycleLegacyStore(t)
	if err := other.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if unexpected, err := other.OpenLifecycleJournal(ctx, path); err == nil {
		_ = unexpected.Close()
		t.Fatal("accepted another database binding")
	}
	if unexpected, err := s.OpenLifecycleJournal(ctx, s.Path); err == nil {
		_ = unexpected.Close()
		t.Fatal("journal aliases app database")
	}
	link := filepath.Join(t.TempDir(), "link.db")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if unexpected, err := s.OpenLifecycleJournal(ctx, link); err == nil {
		_ = unexpected.Close()
		t.Fatal("followed journal symlink")
	}
}

func TestLifecycleJournalCommitProofReplayAndUncertainCrash(t *testing.T) {
	s, j := lifecycleJournalFixture(t)
	ctx := t.Context()
	var incarnation string
	if err := s.SQL.QueryRowContext(ctx, "SELECT incarnation FROM account_incarnations WHERE user_id=1").Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	op := strings.Repeat("a", 32)
	if err := j.PrepareDeletion(ctx, op, incarnation); err != nil {
		t.Fatal(err)
	}
	if err := j.PrepareDeletion(ctx, op, incarnation); err != nil {
		t.Fatal("identical replay", err)
	}
	if err := j.CommitDeletion(ctx, op); err == nil {
		t.Fatal("committed a marker without SQLite deletion proof")
	}
	if err := j.ReconcileIntents(ctx); !errors.Is(err, ErrLifecycleUncertain) {
		t.Fatal("prepared crash must fail closed", err)
	}
	if _, err := s.SQL.ExecContext(ctx, "INSERT INTO lifecycle_operations(operation,incarnation,created_ns) VALUES(?,?,1)", op, incarnation); err != nil {
		t.Fatal(err)
	}
	if err := j.ReconcileIntents(ctx); err != nil {
		t.Fatal("commit proof not recovered", err)
	}
	if err := j.CancelDeletion(ctx, op); err == nil {
		t.Fatal("cancelled committed deletion")
	}
	if err := j.CommitDeletion(ctx, op); err != nil {
		t.Fatal("committed replay", err)
	}
	if marked, err := j.IsDeleted(ctx, incarnation); err != nil || !marked {
		t.Fatal("lost marker", err)
	}
	if err := j.PrepareDeletion(ctx, op, strings.Repeat("b", 32)); err == nil {
		t.Fatal("changed incarnation replay accepted")
	}
	if _, err := j.store.SQL.ExecContext(ctx, "DELETE FROM lifecycle_deletions WHERE operation=?", op); err == nil {
		t.Fatal("append-only marker removed")
	}
	// A snapshot predating the deletion still sees its committed external marker.
	if _, err := s.SQL.ExecContext(ctx, "DELETE FROM lifecycle_operations"); err != nil {
		t.Fatal(err)
	}
	if marked, err := j.IsDeleted(ctx, incarnation); err != nil || !marked {
		t.Fatal("app rollback removed independent marker", err)
	}
}

func TestLifecycleJournalCancelledIntentAndDurableWork(t *testing.T) {
	s, j := lifecycleJournalFixture(t)
	ctx := t.Context()
	var incarnation string
	if err := s.SQL.QueryRowContext(ctx, "SELECT incarnation FROM account_incarnations WHERE user_id=1").Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	op := strings.Repeat("c", 32)
	if err := j.PrepareDeletion(ctx, op, incarnation); err != nil {
		t.Fatal(err)
	}
	if err := j.CancelDeletion(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err := j.ReconcileIntents(ctx); err != nil {
		t.Fatal(err)
	}
	if marked, err := j.IsDeleted(ctx, incarnation); err != nil || marked {
		t.Fatal("cancelled intent deleted account", err)
	}
	target := "mychef/owned-fixture-image"
	if err := j.ReserveWork(ctx, strings.Repeat("d", 32), incarnation, "image", target); err != nil {
		t.Fatal(err)
	}
	if err := j.ReserveWork(ctx, strings.Repeat("d", 32), incarnation, "image", "different"); err == nil {
		t.Fatal("changed work identity accepted")
	}
	path := j.Path
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := s.OpenLifecycleJournal(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	work, err := reopened.WorkFor(ctx, incarnation)
	if err != nil || len(work) != 1 || work[0].Target != target {
		t.Fatal("pending target lost across restart", err)
	}
	if err := reopened.CompleteWork(ctx, work[0].ID); err != nil {
		t.Fatal(err)
	}
	work, err = reopened.WorkFor(ctx, incarnation)
	if err != nil || len(work) != 0 {
		t.Fatal("completed target retained", err)
	}
	var content string
	if err := reopened.store.SQL.QueryRowContext(ctx, "SELECT COALESCE(group_concat(target), '') FROM lifecycle_work").Scan(&content); err != nil || strings.Contains(content, target) {
		t.Fatal("cleanup payload remains readable", err)
	}
}

// The child exits without closing SQLite, exercising durable WAL boundaries
// rather than only returning errors through a live connection.
func TestLifecycleJournalCrashBoundary(t *testing.T) {
	if os.Getenv("MYCHEF_LIFECYCLE_CRASH_HELPER") == "1" {
		ctx := context.Background()
		s, err := Open(os.Getenv("MYCHEF_LIFECYCLE_APP"), false)
		if err != nil {
			t.Fatal(err)
		}
		j, err := s.OpenLifecycleJournal(ctx, os.Getenv("MYCHEF_LIFECYCLE_JOURNAL"))
		if err != nil {
			t.Fatal(err)
		}
		operation, incarnation := os.Getenv("MYCHEF_LIFECYCLE_OPERATION"), os.Getenv("MYCHEF_LIFECYCLE_INCARNATION")
		if err = j.PrepareDeletion(ctx, operation, incarnation); err != nil {
			t.Fatal(err)
		}
		stage := os.Getenv("MYCHEF_LIFECYCLE_STAGE")
		if stage != "prepared" {
			if _, err = s.SQL.ExecContext(ctx, "INSERT INTO lifecycle_operations(operation,incarnation,created_ns) VALUES(?,?,1)", operation, incarnation); err != nil {
				t.Fatal(err)
			}
		}
		if stage == "marker" {
			if err = j.CommitDeletion(ctx, operation); err != nil {
				t.Fatal(err)
			}
		}
		os.Exit(23)
	}
	for _, stage := range []string{"prepared", "sqlite", "marker"} {
		t.Run(stage, func(t *testing.T) {
			s, j := lifecycleJournalFixture(t)
			var incarnation string
			if err := s.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=1").Scan(&incarnation); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			operation := strings.Repeat("e", 32)
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLifecycleJournalCrashBoundary$")
			child.Env = append(os.Environ(), "MYCHEF_LIFECYCLE_CRASH_HELPER=1", "MYCHEF_LIFECYCLE_APP="+s.Path, "MYCHEF_LIFECYCLE_JOURNAL="+j.Path, "MYCHEF_LIFECYCLE_OPERATION="+operation, "MYCHEF_LIFECYCLE_INCARNATION="+incarnation, "MYCHEF_LIFECYCLE_STAGE="+stage)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 23 {
				t.Fatalf("expected injected process exit, got %v: %s", err, output)
			}
			err = j.ReconcileIntents(t.Context())
			if stage == "prepared" {
				if !errors.Is(err, ErrLifecycleUncertain) {
					t.Fatal("uncertain crash did not block readiness", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			deleted, err := j.IsDeleted(t.Context(), incarnation)
			if err != nil || !deleted {
				t.Fatal("committed crash lost deletion marker", err)
			}
		})
	}
}
