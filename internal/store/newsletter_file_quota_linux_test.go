package store

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
)

func TestNewsletterFileCleanupQuotaFailure(t *testing.T) {
	if directory := os.Getenv("MYCHEF_NEWSLETTER_QUOTA_DIR"); directory != "" {
		s, err := Open(filepath.Join(directory, "identity.db"), false)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		if err := s.ConfigureNewsletterIdentity(t.Context(), newsletterIdentityFixtureKey(t, 0)); err != nil {
			t.Fatal(err)
		}
		j, err := s.OpenLifecycleJournal(t.Context(), filepath.Join(directory, "journal.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = j.Close() }()
		// Limit this short-lived process only. SQLite's attempted durable write must
		// fail; ignoring SIGXFSZ lets the real write error reach the maintenance code.
		signal.Ignore(syscall.SIGXFSZ)
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 1024, Max: 1024}); err != nil {
			t.Fatal(err)
		}
		if err := j.CleanupNewsletterFiles(t.Context()); err == nil {
			t.Fatal("file quota failure was reported as completed cleanup")
		}
		return
	}
	s, j, key := newsletterFileFixture(t)
	directory := t.TempDir()
	// Preserve a closed consistent synthetic pair; no snapshot of real data.
	path, journalPath := s.Path, j.Path
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for source, target := range map[string]string{path: "identity.db", journalPath: "journal.db"} {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, target), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestNewsletterFileCleanupQuotaFailure$", "-test.count=1")
	child.Env = []string{"MYCHEF_NEWSLETTER_QUOTA_DIR=" + directory}
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("quota subprocess: %v\n%s", err, output)
	}
	reopened, err := Open(filepath.Join(directory, "identity.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	var pending bool
	if err := reopened.SQL.QueryRowContext(t.Context(), "SELECT physical_cleanup_pending FROM newsletter_identity_namespace").Scan(&pending); err != nil || !pending {
		t.Fatal("quota failure lost durable pending state", err)
	}
	if err := reopened.ConfigureNewsletterIdentity(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.OpenLifecycleJournal(t.Context(), filepath.Join(directory, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recovered.Close() }()
	if err := recovered.CleanupNewsletterFiles(t.Context()); err != nil {
		t.Fatal("unrestricted retry failed", err)
	}
}
