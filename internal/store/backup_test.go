package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/store"
)

func TestOnlineBackupRestoresUsableAccountsAndExactMenus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	source, err := store.Open(filepath.Join(t.TempDir(), "source.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	if err = source.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	journal, err := source.InitializeLifecycleJournal(ctx, source.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	accounts := auth.New(source)
	u, err := accounts.Register(ctx, auth.Registration{Email: "restore@example.test", Username: "restore", FirstName: "Restore", LastName: "Chef", Password: "RestorePass2026!"})
	if err != nil {
		t.Fatal(err)
	}
	operation := store.Generation{UserID: u.ID, Key: "backup", RequestHash: "hash", RequestJSON: "{}", Status: "running", Stage: "saving"}
	if err = source.DB.Create(&operation).Error; err != nil {
		t.Fatal(err)
	}
	quantity := domain.Decimal("1234567890.12345678901234567890")
	recipe := domain.RecipeContent{Title: "Backup supper", Note: "Keep this recipe", Yield: []string{"2 servings"}, Equipment: "Pan", Instructions: []string{"Simmer gently."}, Tips: []string{}, Allergens: []string{}, AllergenNote: "Read labels.", PrepMinutes: 5, CookMinutes: 10, TotalMinutes: 15, Ingredients: []domain.IngredientContent{{Name: "Carrot", Quantity: &quantity}}}
	menu, err := source.SaveMenu(ctx, u.ID, operation.ID, domain.MenuInput{Dates: []string{"2026-10-27"}, Adults: 2, MaxMinutes: 30, Budget: "low", Region: "Belgium", Creativity: 5}, []domain.RecipeContent{recipe})
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := store.LockApplication(source.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	writerDone := make(chan error, 1)
	writerContext, stopWriter := context.WithCancel(ctx)
	defer stopWriter()
	started := make(chan struct{})
	go func() {
		close(started)
		for writerContext.Err() == nil {
			if e := source.DB.WithContext(writerContext).Model(&store.User{}).Where("user_id = ?", u.ID).Update("lastname", "Chef").Error; e != nil && writerContext.Err() == nil {
				writerDone <- e
				return
			}
		}
		writerDone <- nil
	}()
	<-started
	backup := filepath.Join(t.TempDir(), "online.db")
	err = source.Backup(ctx, backup)
	stopWriter()
	if writerErr := <-writerDone; writerErr != nil {
		t.Fatal(writerErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	backupCheck, err := store.OpenReadOnly(backup)
	if err != nil {
		t.Fatal(err)
	}
	var journalMode string
	err = backupCheck.SQL.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode)
	_ = backupCheck.Close()
	if err != nil || journalMode != "delete" {
		t.Fatalf("backup requires WAL sidecars: %s %v", journalMode, err)
	}
	destination := filepath.Join(t.TempDir(), "fresh-volume.db")
	if err = store.Restore(ctx, backup, destination, store.AccountRestoreOptions{JournalPath: journal.Path}); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(destination, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if err = restored.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = auth.New(restored).Login(ctx, "restore@example.test", "RestorePass2026!"); err != nil {
		t.Fatal("restored account cannot sign in")
	}
	after, err := restored.GetMenu(ctx, u.ID, 1)
	if err != nil || after.Recipes[0].Title != menu.Recipes[0].Title || *after.Recipes[0].Ingredients[0].Quantity != string(quantity) {
		t.Fatalf("restored menu lost fields: %+v %v", after, err)
	}
	if err = store.Restore(ctx, backup, destination, store.AccountRestoreOptions{JournalPath: journal.Path}); err == nil {
		t.Fatal("restore overwrote an existing database")
	}
	if err = source.Backup(ctx, backup); err == nil {
		t.Fatal("backup overwrote an existing database")
	}
}

func TestRestoreRejectsUnversionedDatabaseAndActiveDestination(t *testing.T) {
	ctx := context.Background()
	source, err := store.Open(filepath.Join(t.TempDir(), "invalid.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err = source.DB.Exec("CREATE TABLE unrelated(id INTEGER)").Error; err != nil {
		t.Fatal(err)
	}
	_ = source.Close()
	destination := filepath.Join(t.TempDir(), "restored.db")
	if err = store.Restore(ctx, source.Path, destination); err == nil {
		t.Fatal("unversioned backup restored")
	}
	unlock, err := store.LockApplication(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err = store.Restore(ctx, source.Path, destination); err == nil {
		t.Fatal("active destination restored")
	}
}
