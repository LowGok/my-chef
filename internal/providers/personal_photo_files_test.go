package providers

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersonalFixturePhotosPersistNormalizedObjectsAcrossRestart(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "objects")
	first, err := NewFixturePersonalPhotoFiles(directory)
	if err != nil {
		t.Fatal(err)
	}
	photo, err := NormalizePersonalPhoto(photoFixture(t, 24, 12, "png"))
	if err != nil {
		t.Fatal(err)
	}
	target := "mychef/" + strings.Repeat("a", 32)
	call := WithUploadIntent(t.Context(), target, func(_ context.Context) error { return nil })
	object, err := first.UploadPersonalPhoto(call, photo)
	if err != nil || object.Target != target {
		t.Fatal("fixture upload failed", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := NewFixturePersonalPhotoFiles(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	data, err := second.ReadPersonalPhoto(t.Context(), target, photo.Digest())
	if err != nil || !bytes.Equal(data, photo.JPEG()) {
		t.Fatal("normalized photo lost on provider restart", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != strings.Repeat("a", 32)+".jpg" {
		t.Fatal("unexpected fixture storage names", err)
	}
	stat, err := os.Stat(filepath.Join(directory, entries[0].Name()))
	if err != nil || stat.Mode().Perm() != 0o600 {
		t.Fatal("fixture photo not private", err)
	}
	stat, err = os.Stat(directory)
	if err != nil || stat.Mode().Perm() != 0o700 {
		t.Fatal("fixture directory not private", err)
	}
	if _, err := second.UploadPersonalPhoto(call, photo); err == nil {
		t.Fatal("existing fixture target overwritten")
	}
	if _, err := second.ReadPersonalPhoto(t.Context(), target, strings.Repeat("0", 64)); err == nil {
		t.Fatal("changed digest accepted")
	}
	result, err := second.DeletePersonalPhoto(t.Context(), target)
	if err != nil || !result.Deleted {
		t.Fatal("typed fixture deletion failed", err)
	}
	result, err = second.DeletePersonalPhoto(t.Context(), target)
	if err != nil || !result.Missing {
		t.Fatal("idempotent fixture deletion failed", err)
	}
}

func TestPersonalFixturePhotosRejectUnsafePathsAndEntries(t *testing.T) {
	directory := t.TempDir()
	for _, path := range []string{"relative", directory + "/../unsafe/..", ""} {
		if store, err := NewFixturePersonalPhotoFiles(path); err == nil {
			_ = store.Close()
			t.Fatal("unsafe fixture directory accepted")
		}
	}
	public := filepath.Join(directory, "public")
	if err := os.Mkdir(public, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0o755); err != nil {
		t.Fatal(err)
	}
	if store, err := NewFixturePersonalPhotoFiles(public); err == nil {
		_ = store.Close()
		t.Fatal("public directory accepted")
	}
	target := filepath.Join(directory, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if store, err := NewFixturePersonalPhotoFiles(link); err == nil {
		_ = store.Close()
		t.Fatal("symlinked directory accepted")
	}
	if err := os.WriteFile(filepath.Join(target, "unrelated.txt"), []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if store, err := NewFixturePersonalPhotoFiles(target); err == nil {
		_ = store.Close()
		t.Fatal("unrelated files accepted as a fixture provider")
	}
}
