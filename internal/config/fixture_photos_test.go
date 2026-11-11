package config

import (
	"path/filepath"
	"testing"
)

func TestFixturePhotoDirectoryNeedsValidatedFixtureRuntime(t *testing.T) {
	t.Setenv("MYCHEF_FIXTURE_PHOTOS_DIR", "")
	cfg := Config{Provider: "fixture"}
	settings := &FixtureInbox{}
	if path, err := LoadFixturePhotoDirectory(cfg, nil); err != nil || path != "" {
		t.Fatal("fixture photos enabled by default")
	}
	path := filepath.Join(t.TempDir(), "provider")
	t.Setenv("MYCHEF_FIXTURE_PHOTOS_DIR", path)
	t.Setenv("MYCHEF_DEV", "1")
	t.Setenv("MYCHEF_E2E", "1")
	if _, err := LoadFixturePhotoDirectory(cfg, nil); err == nil {
		t.Fatal("unvalidated fixture configured file provider")
	}
	if got, err := LoadFixturePhotoDirectory(cfg, settings); err != nil || got != path {
		t.Fatal("isolated fixture provider rejected", err)
	}
	for _, key := range []string{"MYCHEF_DEV", "MYCHEF_E2E"} {
		t.Setenv(key, "0")
		if _, err := LoadFixturePhotoDirectory(cfg, settings); err == nil {
			t.Fatal("fixture provider escaped its runtime", key)
		}
		t.Setenv(key, "1")
	}
	cfg.Provider = "gemini"
	if _, err := LoadFixturePhotoDirectory(cfg, settings); err == nil {
		t.Fatal("normal provider accepted fixture photo storage")
	}
	cfg.Provider = "fixture"
	for _, path := range []string{"relative", "/", "/tmp/../tmp/photo"} {
		t.Setenv("MYCHEF_FIXTURE_PHOTOS_DIR", path)
		if _, err := LoadFixturePhotoDirectory(cfg, settings); err == nil {
			t.Fatal("unsafe fixture directory accepted")
		}
	}
}
