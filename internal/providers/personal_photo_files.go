package providers

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/LowGok/my-chef/internal/store"
)

// FixturePersonalPhotoFiles is an isolated fake object provider, not an app
// upload directory. It stores only normalized synthetic JPEGs outside SQLite so
// restart and restore tests can keep their fake provider independently alive.
type FixturePersonalPhotoFiles struct {
	mu    sync.Mutex
	root  *os.Root
	count int
}

func NewFixturePersonalPhotoFiles(directory string) (*FixturePersonalPhotoFiles, error) {
	invalid := Failure("provider_invalid")
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == string(filepath.Separator) {
		return nil, invalid
	}
	if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, invalid
	}
	stat, err := os.Lstat(directory)
	if err != nil || !stat.IsDir() || stat.Mode().Perm() != 0o700 {
		return nil, invalid
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return nil, invalid
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, invalid
	}
	fail := func() (*FixturePersonalPhotoFiles, error) { _ = root.Close(); return nil, invalid }
	file, err := root.Open(".")
	if err != nil {
		return fail()
	}
	entries, err := file.ReadDir(129)
	_ = file.Close()
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 128 {
		return fail()
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(name, ".jpg") || !validPersonalPhotoTarget("mychef/"+strings.TrimSuffix(name, ".jpg")) {
			return fail()
		}
		metadata, err := entry.Info()
		if err != nil || !metadata.Mode().IsRegular() || metadata.Mode().Perm() != 0o600 || metadata.Size() < 1 || metadata.Size() > PersonalPhotoLimit {
			return fail()
		}
	}
	return &FixturePersonalPhotoFiles{root: root, count: len(entries)}, nil
}
func (*FixturePersonalPhotoFiles) String() string       { return "private synthetic photo provider" }
func (*FixturePersonalPhotoFiles) GoString() string     { return "private synthetic photo provider" }
func (*FixturePersonalPhotoFiles) AssetBinding() string { return "fixture" }
func (p *FixturePersonalPhotoFiles) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.root.Close()
}
func (*FixturePersonalPhotoFiles) DeleteImage(context.Context, string) (store.AssetDeletionResult, error) {
	return store.AssetDeletionResult{Missing: true}, nil
}

func (p *FixturePersonalPhotoFiles) UploadPersonalPhoto(ctx context.Context, photo NormalizedPhoto) (PrivatePhotoObject, error) {
	intent, ok := ctx.Value(uploadIntentKey{}).(uploadIntent)
	if !ok || intent.begin == nil || !validPersonalPhotoTarget(intent.target) || photo.Size() == 0 {
		return PrivatePhotoObject{}, Failure("provider_invalid")
	}
	if err := StartReservedUpload(ctx); err != nil {
		return PrivatePhotoObject{}, Failure("provider_unavailable")
	}
	if err := ctx.Err(); err != nil {
		return PrivatePhotoObject{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.count >= 128 {
		return PrivatePhotoObject{}, Failure("provider_quota")
	}
	name := strings.TrimPrefix(intent.target, "mychef/") + ".jpg"
	file, err := p.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return PrivatePhotoObject{}, Failure("provider_unavailable")
	}
	p.count++
	data := photo.JPEG()
	_, err = file.Write(data)
	clear(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		if p.root.Remove(name) == nil {
			p.count--
		}
		return PrivatePhotoObject{}, Failure("provider_unavailable")
	}
	directory, err := p.root.Open(".")
	if err == nil {
		err = directory.Sync()
		_ = directory.Close()
	}
	if err != nil {
		return PrivatePhotoObject{}, Failure("provider_unavailable")
	}
	return PrivatePhotoObject{Target: intent.target, Binding: "fixture", DeliveryType: "authenticated", Digest: photo.Digest()}, nil
}
func (p *FixturePersonalPhotoFiles) ReadPersonalPhoto(ctx context.Context, target, digest string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validPersonalPhotoTarget(target) {
		return nil, Failure("provider_invalid")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	file, err := p.root.OpenFile(strings.TrimPrefix(target, "mychef/")+".jpg", os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, Failure("provider_unavailable")
	}
	defer func() { _ = file.Close() }()
	metadata, err := file.Stat()
	if err != nil || !metadata.Mode().IsRegular() || metadata.Mode().Perm() != 0o600 || metadata.Size() < 1 || metadata.Size() > PersonalPhotoLimit {
		return nil, Failure("provider_unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(file, PersonalPhotoLimit+1))
	if err != nil {
		clear(data)
		return nil, Failure("provider_unavailable")
	}
	normalized, err := normalizedPersonalJPEG(data)
	if err != nil || normalized.Digest() != digest || ctx.Err() != nil {
		clear(data)
		return nil, Failure("provider_unavailable")
	}
	return data, nil
}
func (p *FixturePersonalPhotoFiles) DeletePersonalPhoto(ctx context.Context, target string) (store.AssetDeletionResult, error) {
	if err := ctx.Err(); err != nil {
		return store.AssetDeletionResult{}, err
	}
	if !validPersonalPhotoTarget(target) {
		return store.AssetDeletionResult{Permanent: true}, Failure("provider_invalid")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.root.Remove(strings.TrimPrefix(target, "mychef/") + ".jpg")
	if errors.Is(err, os.ErrNotExist) {
		return store.AssetDeletionResult{Missing: true}, nil
	}
	if err != nil {
		return store.AssetDeletionResult{}, Failure("provider_unavailable")
	}
	p.count = max(0, p.count-1)
	return store.AssetDeletionResult{Deleted: true}, nil
}
