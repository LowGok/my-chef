package menus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
)

type PersonalPhotoNormalizer interface {
	Normalize(context.Context, []byte) (providers.NormalizedPhoto, error)
}
type PersonalPhotoUploader interface {
	AssetBinding() string
	UploadPersonalPhoto(context.Context, providers.NormalizedPhoto) (providers.PrivatePhotoObject, error)
}
type PersonalRecipes struct {
	store      *store.Store
	journal    *store.LifecycleJournal
	reader     store.PersonalEntitlementReader
	normalizer PersonalPhotoNormalizer
	photos     PersonalPhotoUploader
}

func NewPersonalRecipes(s *store.Store, j *store.LifecycleJournal, reader store.PersonalEntitlementReader, normalizer PersonalPhotoNormalizer, photos PersonalPhotoUploader) *PersonalRecipes {
	return &PersonalRecipes{store: s, journal: j, reader: reader, normalizer: normalizer, photos: photos}
}

func (s *PersonalRecipes) Save(ctx context.Context, actor store.PersonalActor, input store.PersonalSave, raw []byte, source string) (store.PersonalRecipe, error) {
	content, err := input.Content.Normalize()
	if err != nil {
		return store.PersonalRecipe{}, err
	}
	input.Content = content
	if s.store == nil || s.journal == nil {
		return store.PersonalRecipe{}, store.ErrPersonalStorage
	}
	if input.PhotoAction != "replace" {
		if len(raw) != 0 || input.PhotoDigest != "" {
			return store.PersonalRecipe{}, store.ErrPersonalInput
		}
		s.store.AccountWork.RLock()
		defer s.store.AccountWork.RUnlock()
		return s.store.SavePersonalRecipe(ctx, actor, s.reader, input, time.Now())
	}
	if s.normalizer == nil || s.photos == nil {
		return store.PersonalRecipe{}, store.ErrPersonalPhotoFailed
	}
	tracked, release, err := s.journal.TrackAccountWork(ctx, actor.UserID)
	if err != nil {
		return store.PersonalRecipe{}, err
	}
	defer release()
	hash := sha256.Sum256(raw)
	input.PhotoDigest = hex.EncodeToString(hash[:])
	admission, err := s.journal.AdmitPersonalPhoto(tracked, actor, s.reader, input, source, time.Now())
	if err != nil {
		return store.PersonalRecipe{}, err
	}
	if !admission.Dispatch {
		return s.store.PersonalPhotoOutcome(tracked, actor, admission, time.Now())
	}
	defer func() {
		finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.journal.FailPersonalPhotoAdmission(finish, actor, admission)
	}()
	photo, err := s.normalizer.Normalize(tracked, raw)
	if err != nil {
		return store.PersonalRecipe{}, err
	}
	input.PhotoDigest = photo.Digest()
	width, height := photo.Dimensions()
	upload := store.PersonalPhotoUpload{Binding: s.photos.AssetBinding(), Digest: photo.Digest(), Width: width, Height: height, Size: photo.Size()}
	reservation, err := s.journal.ReservePersonalPhoto(tracked, actor, s.reader, input, upload, source, time.Now(), admission)
	if err != nil {
		return store.PersonalRecipe{}, err
	}
	if !reservation.Dispatch {
		return s.store.PersonalPhotoOutcome(tracked, actor, admission, time.Now())
	}
	reserved := providers.WithUploadIntent(tracked, reservation.Work.Target, func(call context.Context) error {
		return s.journal.BeginPersonalPhoto(call, actor, s.reader, input, reservation, time.Now())
	})
	object, uploadErr := s.photos.UploadPersonalPhoto(reserved, photo)
	confirmed := uploadErr == nil && object.Target == reservation.Work.Target && object.Binding == reservation.Work.Provider && object.DeliveryType == "authenticated" && object.Digest == photo.Digest()
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	result, err := s.journal.FinishPersonalPhoto(finish, actor, s.reader, input, reservation, confirmed, time.Now(), store.PersonalWriteOptions{Abort: tracked.Err() != nil})
	if tracked.Err() != nil {
		return store.PersonalRecipe{}, store.ErrPersonalPhotoFailed
	}
	return result, err
}
