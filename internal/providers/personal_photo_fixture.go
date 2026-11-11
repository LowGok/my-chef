package providers

import (
	"context"
	"sync"

	"github.com/LowGok/my-chef/internal/store"
)

// FixturePersonalPhotos keeps only normalized synthetic images in memory. It is
// injected solely by the existing isolated DEV/E2E fixture runtime.
type FixturePersonalPhotos struct {
	mu     sync.Mutex
	images map[string]NormalizedPhoto
}

func NewFixturePersonalPhotos() *FixturePersonalPhotos {
	return &FixturePersonalPhotos{images: map[string]NormalizedPhoto{}}
}
func (*FixturePersonalPhotos) AssetBinding() string { return "fixture" }
func (*FixturePersonalPhotos) DeleteImage(context.Context, string) (store.AssetDeletionResult, error) {
	return store.AssetDeletionResult{Missing: true}, nil
}
func (p *FixturePersonalPhotos) UploadPersonalPhoto(ctx context.Context, photo NormalizedPhoto) (PrivatePhotoObject, error) {
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
	if _, exists := p.images[intent.target]; exists || len(p.images) >= 128 {
		return PrivatePhotoObject{}, Failure("provider_quota")
	}
	p.images[intent.target] = photo
	return PrivatePhotoObject{Target: intent.target, Binding: "fixture", DeliveryType: "authenticated", Digest: photo.Digest()}, nil
}
func (p *FixturePersonalPhotos) ReadPersonalPhoto(ctx context.Context, target, digest string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	photo, exists := p.images[target]
	if !exists || !validPersonalPhotoTarget(target) || photo.Digest() != digest {
		return nil, Failure("provider_unavailable")
	}
	return photo.JPEG(), nil
}
func (p *FixturePersonalPhotos) DeletePersonalPhoto(ctx context.Context, target string) (store.AssetDeletionResult, error) {
	if err := ctx.Err(); err != nil {
		return store.AssetDeletionResult{}, err
	}
	if !validPersonalPhotoTarget(target) {
		return store.AssetDeletionResult{Permanent: true}, Failure("provider_invalid")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, exists := p.images[target]
	delete(p.images, target)
	return store.AssetDeletionResult{Deleted: exists, Missing: !exists}, nil
}
