package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecipeExtractionPolicyPrivateRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(path, []byte(`{"enabled":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	policy, err := LoadRecipeExtractionPolicy(path)
	if err != nil || policy.Enabled {
		t.Fatal("private kill policy unreadable", err)
	}
	for _, body := range []string{`{"enabled":true,"resetLedger":true}`, `{"enabled":false} {}`, strings.Repeat("x", 65537)} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRecipeExtractionPolicy(path); err == nil {
			t.Fatal("unknown or malformed policy accepted")
		}
	}
	if err := os.WriteFile(path, []byte(`{"enabled":false}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRecipeExtractionPolicy(path); err == nil {
		t.Fatal("non-private policy accepted")
	}
	alias := filepath.Join(dir, "alias.json")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRecipeExtractionPolicy(alias); err == nil {
		t.Fatal("symlink policy accepted")
	}
	if _, err := LoadRecipeExtractionPolicy(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing policy accepted")
	}
}

func TestRecipeImportClientNeedsExplicitPrivateConfiguration(t *testing.T) {
	for _, name := range []string{"MYCHEF_IMPORT_WORKER_SOCKET", "MYCHEF_IMPORT_CHANNEL_KEY_FILE", "MYCHEF_IMPORT_TEMP_DIR"} {
		t.Setenv(name, "")
	}
	if value, err := LoadRecipeImportClient(); err != nil || value != nil {
		t.Fatal("optional imports changed ordinary startup", err)
	}
	t.Setenv("MYCHEF_IMPORT_WORKER_SOCKET", "/run/imports/import-worker.sock")
	if _, err := LoadRecipeImportClient(); err == nil {
		t.Fatal("partial import configuration enabled")
	}
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "channel.key")
	if err := os.WriteFile(keyPath, bytes.Repeat([]byte{41}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYCHEF_IMPORT_CHANNEL_KEY_FILE", keyPath)
	t.Setenv("MYCHEF_IMPORT_TEMP_DIR", filepath.Join(directory, "sources"))
	settings, err := LoadRecipeImportClient()
	if err != nil || settings == nil || len(settings.ChannelKey) != 32 {
		t.Fatal("complete import configuration rejected", err)
	}
	t.Setenv("MYCHEF_IMPORT_WORKER_SOCKET", "https://public.example.com/proxy")
	if _, err := LoadRecipeImportClient(); err == nil {
		t.Fatal("public proxy replaced private worker")
	}
}
