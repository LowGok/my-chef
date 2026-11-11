package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

func TestAccountCleanupCommandIsReadOnlyAndDoesNotPrintTargets(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(filepath.Join(t.TempDir(), "app.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	user := store.User{FirstName: "Private", LastName: "Fixture", Email: "private-contact@example.test", Username: "private-fixture", Password: "private-password-hash"}
	if err = s.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Create(&store.Meal{ID: 1, CreatorID: user.ID, CreationDate: "2026-11-09", RecipeTitle: "Private", ImageStatus: "pending"}).Error; err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Create(&store.Session{Hash: "private-session-hash", UserID: &user.ID, ExpiresUnix: time.Now().Add(time.Hour).Unix()}).Error; err != nil {
		t.Fatal(err)
	}
	journal, err := s.InitializeLifecycleJournal(ctx, s.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	work, err := journal.ReserveRecipeImage(ctx, user.ID, 1, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = journal.BeginImageUpload(ctx, work.ID, work.Target); err != nil {
		t.Fatal(err)
	}
	if err = journal.FinishImageUpload(ctx, work.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.DeleteAccount(ctx, store.AccountActor{UserID: user.ID, SessionHash: "private-session-hash", PasswordHash: user.Password}, strings.Repeat("f", 32), "DELETE", time.Now(), store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(journal.Path)
	if err != nil {
		t.Fatal(err)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = write
	result := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(read); _ = read.Close(); result <- data }()
	code := runAccountCleanupStatus([]string{"--db", s.Path, "--lifecycle-db", journal.Path})
	os.Stdout = previous
	_ = write.Close()
	output := <-result
	if code != 0 || !bytes.Contains(output, []byte(work.ID)) {
		t.Fatal("status command failed")
	}
	for _, secret := range []string{work.Target, strings.TrimPrefix(work.Target, "mychef/"), work.Incarnation, user.Email, user.Password, "private-session-hash"} {
		if bytes.Contains(output, []byte(secret)) {
			t.Fatal("routine status printed protected data")
		}
	}
	after, err := os.ReadFile(journal.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read-only command changed journal", err)
	}
	if code := runAccountCleanupStatus([]string{"--db", s.Path, "--lifecycle-db", journal.Path, "--limit", "101"}); code != 2 {
		t.Fatal("unbounded private lookup accepted")
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	if code := runAccountCleanupStatus([]string{"--db", s.Path, "--lifecycle-db", missing}); code != 1 {
		t.Fatal("missing journal accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("read-only command created a journal")
	}
}
