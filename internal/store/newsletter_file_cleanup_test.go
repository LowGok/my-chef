package store

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func newsletterFileFixture(t *testing.T) (*Store, *LifecycleJournal, *NewsletterIdentityKey) {
	t.Helper()
	s, j, key, record := newsletterIdentityJournalFixture(t)
	commitNewsletterIdentityFixture(t, j, strings.Repeat("a", 32), record)
	if err := j.CompleteNewsletterIdentityBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	minimisationSQL(t, s, "UPDATE newsletter_identity_namespace SET physical_cleanup_pending=1")
	return s, j, key
}

func assertFileCanaries(t *testing.T, s *Store, absent, present string) {
	t.Helper()
	foundControl := false
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(s.Path + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(absent)) {
			t.Fatal("obsolete synthetic bytes remain in active file", suffix)
		}
		foundControl = foundControl || bytes.Contains(data, []byte(present))
	}
	if !foundControl {
		t.Fatal("still-authorized control bytes disappeared")
	}
	var control string
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT value FROM retained_control").Scan(&control); err != nil || control != present {
		t.Fatal("authorized control changed", err)
	}
}

func TestNewsletterFileCleanupSecureDeleteOnEveryWritableConnection(t *testing.T) {
	s, j, _ := newsletterFileFixture(t)
	for _, db := range []*Store{s, j.store} {
		for range 2 {
			var enabled int
			if err := db.SQL.QueryRowContext(t.Context(), "PRAGMA secure_delete").Scan(&enabled); err != nil || enabled != 1 {
				t.Fatal("writable connection does not securely delete", err)
			}
			db.SQL.SetMaxIdleConns(0)
			// Returning to one idle connection must still use the configured DSN.
			db.SQL.SetMaxIdleConns(1)
		}
	}
}

func TestNewsletterFileCleanupLegacyFreelistAndWAL(t *testing.T) {
	s, j, _ := newsletterFileFixture(t)
	const expired = "expired-address-body-canary@example.test"
	const control = "confirmed-subscriber-control@example.test"
	for _, db := range []*Store{s, j.store} {
		minimisationSQL(t, db, "PRAGMA secure_delete=OFF")
		minimisationSQL(t, db, "CREATE TABLE obsolete_plaintext(value TEXT); CREATE TABLE retained_control(value TEXT)")
		minimisationSQL(t, db, "INSERT INTO retained_control VALUES(?)", control)
		minimisationSQL(t, db, "INSERT INTO obsolete_plaintext VALUES(?)", strings.Repeat(expired, 10000))
		minimisationSQL(t, db, "PRAGMA wal_checkpoint(TRUNCATE)")
		minimisationSQL(t, db, "DELETE FROM obsolete_plaintext")
		data, err := os.ReadFile(db.Path)
		if err != nil || !bytes.Contains(data, []byte(expired)) {
			t.Fatal("fixture did not establish legacy plaintext remnants", err)
		}
	}
	if err := j.CleanupNewsletterFiles(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, db := range []*Store{s, j.store} {
		assertFileCanaries(t, db, expired, control)
	}
	if err := j.NewsletterFilesReady(t.Context()); err != nil {
		t.Fatal("completed maintenance not ready", err)
	}
	if err := s.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := j.CleanupNewsletterFiles(t.Context()); err != nil {
		t.Fatal("repeated maintenance changed identity", err)
	}
}

func TestNewsletterFileCleanupBusyReaderRetainsPendingAndRetries(t *testing.T) {
	s, j, _ := newsletterFileFixture(t)
	reader, err := OpenReadOnly(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	held, err := reader.SQL.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var total int
	if err := held.QueryRowContext(t.Context(), "SELECT count(*) FROM users").Scan(&total); err != nil {
		t.Fatal(err)
	}
	minimisationSQL(t, s, "UPDATE newsletter_identity_namespace SET physical_cleanup_pending=1")
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	started := time.Now()
	err = j.CleanupNewsletterFiles(ctx)
	cancel()
	if err == nil || time.Since(started) > 6*time.Second {
		t.Fatal("busy maintenance was accepted or exceeded its bound", err)
	}
	if err := j.NewsletterFilesReady(t.Context()); err == nil {
		t.Fatal("busy physical cleanup became ready")
	}
	if err := held.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := j.CleanupNewsletterFiles(t.Context()); err != nil {
		t.Fatal("reader-release retry failed", err)
	}
	if err := j.NewsletterFilesReady(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestNewsletterFileCleanupCancellationStorageFailureAndRestart(t *testing.T) {
	s, j, key := newsletterFileFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := j.CleanupNewsletterFiles(ctx); err == nil {
		t.Fatal("cancelled cleanup was accepted")
	}
	// Real SQLite read-only failure, without touching global disk quotas or any
	// other app. Container storage-full proof belongs to the process rehearsal.
	minimisationSQL(t, s, "PRAGMA query_only=ON")
	if err := j.CleanupNewsletterFiles(t.Context()); err == nil {
		t.Fatal("storage failure was reported as success")
	}
	minimisationSQL(t, s, "PRAGMA query_only=OFF")
	if err := j.NewsletterFilesReady(t.Context()); err == nil {
		t.Fatal("failed maintenance cleared the durable pending marker")
	}
	path, journalPath := s.Path, j.Path
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if err := reopened.ConfigureNewsletterIdentity(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	journal, err := reopened.OpenLifecycleJournal(t.Context(), journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	if err := journal.NewsletterFilesReady(t.Context()); err == nil {
		t.Fatal("restart forgot unfinished physical cleanup")
	}
	if err := journal.CleanupNewsletterFiles(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := journal.NewsletterFilesReady(t.Context()); err != nil {
		t.Fatal(err)
	}
}
