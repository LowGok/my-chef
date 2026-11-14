package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/LowGok/my-chef/internal/analytics"
	"github.com/LowGok/my-chef/internal/store"
)

func TestLivePreparationBackupAndAppRestoreStayDisabled(t *testing.T) {
	t.Setenv("MYCHEF_EMAIL_PROOF_KEYS_FILE", "")
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	appPath := filepath.Join(dir, "app.sqlite")
	app, e := store.Open(appPath, true)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Migrate(t.Context()); e != nil {
		t.Fatal(e)
	}
	journal, e := app.InitializeLifecycleJournal(t.Context(), appPath+".lifecycle")
	if e != nil {
		t.Fatal(e)
	}
	_ = journal.Close()
	_ = app.Close()
	key := filepath.Join(dir, "backup.key")
	if e = os.WriteFile(key, bytes.Repeat([]byte{3}, 32), 0600); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(dir, "operator.json")
	root := filepath.Join(dir, "analytics")
	b, _ := json.Marshal(analytics.RuntimeFile{Root: root, PrivacyPath: filepath.Join(dir, "privacy.sqlite"), AnchorPath: filepath.Join(dir, "anchor"), BackupKeyFile: key})
	if e = os.WriteFile(file, b, 0600); e != nil {
		t.Fatal(e)
	}
	var out, diagnostic bytes.Buffer
	if e = liveCommand(t.Context(), []string{"init", "--config", file, "--app", appPath}, &out, &diagnostic); e != nil {
		t.Fatal(e)
	}
	if e = liveCommand(t.Context(), []string{"refresh", "--config", file, "--app", appPath}, &out, &diagnostic); e == nil {
		t.Fatal("unassessed live refresh accepted")
	}
	backup := filepath.Join(dir, "app-backup.sqlite")
	if e = liveCommand(t.Context(), []string{"backup-app", "--config", file, "--app", appPath, "--destination", backup, "--lifecycle-db", appPath + ".lifecycle"}, &out, &diagnostic); e != nil {
		t.Fatal(e)
	}
	destination := filepath.Join(dir, "restored.sqlite")
	if e = liveCommand(t.Context(), []string{"restore-app", "--config", file, "--app", destination, "--copy", backup, "--lifecycle-db", appPath + ".lifecycle"}, &out, &diagnostic); e != nil {
		t.Fatal(e)
	}
	restored, e := store.Open(destination, false)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = restored.Close() }()
	var n int
	if e = restored.SQL.QueryRow("SELECT count(*) FROM analytics_outbox").Scan(&n); e != nil || n != 0 {
		t.Fatal("preparation collected live events", e)
	}
}
