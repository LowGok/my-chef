package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
)

var (
	ErrPersonalPhotoLimit  = errors.New("photo save limit reached; try again later")
	ErrPersonalPhotoBusy   = errors.New("photo saves are busy; try again shortly")
	ErrPersonalPhotoFailed = errors.New("the photo was not saved; choose retry or remove it")
)

type PersonalPhotoUpload struct {
	Binding, Digest     string
	Width, Height, Size int
}
type PersonalPhotoReservation struct {
	AssetID  string
	Work     LifecycleWork
	Dispatch bool
}
type PersonalPhotoAdmission struct {
	RequestKey, RequestHash, Incarnation string
	Dispatch                             bool
}
type personalPhotoRequest struct {
	UserID                                                   int64
	Incarnation, RequestKey, RequestHash, ContentHash, State string
	LeaseNS                                                  int64
}

func (personalPhotoRequest) TableName() string { return "personal_photo_requests" }

type PersonalPhotoAsset struct {
	ID, Incarnation, Target, Binding, DeliveryType, Digest string `json:"-"`
	UserID                                                 int64  `json:"-"`
	Width, Height, Size                                    int    `json:"-"`
	CreatedNS                                              int64  `json:"-"`
}

func (PersonalPhotoAsset) TableName() string { return "personal_photo_assets" }
func (PersonalPhotoAsset) String() string    { return "private photo identity" }
func (PersonalPhotoAsset) GoString() string  { return "private photo identity" }

type personalPhotoAttempt struct {
	UserID                                                       int64
	Incarnation, RequestKey, RequestHash, AssetID, WorkID, State string
	LeaseNS, CreatedNS                                           int64
}

func (personalPhotoAttempt) TableName() string { return "personal_photo_attempts" }

func validPersonalTarget(value string) bool {
	return strings.HasPrefix(value, "mychef/") && validPersonalID(strings.TrimPrefix(value, "mychef/"))
}
func validPersonalDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == hex.EncodeToString(decoded)
}

func preparePersonalVersionPhoto(tx *gorm.DB, actor PersonalActor, incarnation string, input PersonalSave, version *personalVersion, options []PersonalWriteOptions) error {
	switch input.PhotoAction {
	case "replace":
		if len(options) == 0 || !validPersonalID(options[0].PhotoAssetID) {
			return ErrPersonalInput
		}
		var attempt personalPhotoAttempt
		hash, err := personalRequestHash(input)
		if err != nil {
			return err
		}
		if err := tx.Where("user_id=? AND incarnation=? AND request_key=? AND request_hash=? AND asset_id=? AND state='uploading'", actor.UserID, incarnation, input.RequestKey, hash, options[0].PhotoAssetID).Take(&attempt).Error; err != nil {
			return ErrPersonalPhotoFailed
		}
		var asset PersonalPhotoAsset
		if err := tx.Where("id=? AND user_id=? AND incarnation=? AND digest=?", attempt.AssetID, actor.UserID, incarnation, input.PhotoDigest).Take(&asset).Error; err != nil {
			return ErrPersonalPhotoFailed
		}
		version.PhotoID = Ptr(asset.ID)
	case "remove":
		return nil
	default:
		if input.ID != "" {
			var previous personalVersion
			if err := tx.Where("recipe_id=? AND id=?", input.ID, input.ExpectedVersion).Take(&previous).Error; err != nil {
				return ErrPersonalStorage
			}
			version.PhotoID, version.ImageURL, version.ImagePublicID, version.ImageBinding = previous.PhotoID, previous.ImageURL, previous.ImagePublicID, previous.ImageBinding
		} else if input.SourceRecipeID != nil {
			source, err := personalCopySource(tx, actor.UserID, *input.SourceRecipeID)
			if err != nil {
				return err
			}
			if source.ImageURL == nil {
				return nil
			}
			if strings.HasPrefix(*source.ImageURL, "/api/v1/personal-photos/") {
				id := strings.TrimPrefix(*source.ImageURL, "/api/v1/personal-photos/")
				var asset PersonalPhotoAsset
				if validPersonalID(id) && tx.Where("id=? AND user_id=? AND incarnation=?", id, actor.UserID, incarnation).Take(&asset).Error == nil {
					version.PhotoID = Ptr(id)
				}
				return nil
			}
			parsed, err := url.Parse(*source.ImageURL)
			binding := assetURLBinding(source.ImageURL)
			if err == nil && parsed.Scheme == "https" && parsed.RawQuery == "" && parsed.Fragment == "" && binding != "unknown" {
				version.ImageURL, version.ImageBinding = source.ImageURL, Ptr(binding)
			}
		}
	}
	return nil
}

func (s *Store) ReadPersonalPhoto(ctx context.Context, actor PersonalActor, id string, now time.Time) (PersonalPhotoAsset, error) {
	if !validPersonalID(id) {
		return PersonalPhotoAsset{}, ErrPersonalNotFound
	}
	var asset PersonalPhotoAsset
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		err = tx.Where("id=? AND user_id=? AND incarnation=? AND ("+personalPhotoReferences+")", id, actor.UserID, incarnation).Take(&asset).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPersonalNotFound
		}
		if err != nil {
			return ErrPersonalStorage
		}
		return nil
	})
	return asset, err
}

func personalPhotoBudget(tx *gorm.DB, incarnation, source string, now time.Time) error {
	var key string
	if err := tx.Raw("SELECT key FROM personal_photo_limit_key WHERE singleton=1").Row().Scan(&key); err != nil {
		return ErrPersonalStorage
	}
	secret, err := hex.DecodeString(key)
	if err != nil || len(secret) != 32 {
		return ErrPersonalStorage
	}
	hash := hmac.New(sha256.New, secret)
	_, _ = hash.Write([]byte(source))
	keys := []struct {
		key     string
		maximum int
	}{{"account:" + incarnation, 10}, {"source:" + hex.EncodeToString(hash.Sum(nil)), 20}}
	if err := tx.Exec("DELETE FROM personal_photo_limits WHERE started_ns<=?", now.Add(-time.Hour).UnixNano()).Error; err != nil {
		return ErrPersonalStorage
	}
	for _, limit := range keys {
		var count int
		if err := tx.Raw("SELECT COALESCE((SELECT count FROM personal_photo_limits WHERE key=?),0)", limit.key).Row().Scan(&count); err != nil {
			return ErrPersonalStorage
		}
		if count >= limit.maximum {
			return ErrPersonalPhotoLimit
		}
	}
	for _, limit := range keys {
		if err := tx.Exec("INSERT INTO personal_photo_limits VALUES(?,?,1) ON CONFLICT(key) DO UPDATE SET count=count+1", limit.key, now.UnixNano()).Error; err != nil {
			return ErrPersonalStorage
		}
	}
	return nil
}

func personalSaveAvailable(ctx context.Context, tx *gorm.DB, actor PersonalActor, reader PersonalEntitlementReader, input PersonalSave, now time.Time) (string, error) {
	incarnation, err := personalAuthority(tx, actor, now)
	if err != nil {
		return "", err
	}
	if err := personalPermission(ctx, tx, actor, incarnation, reader, now); err != nil {
		return "", err
	}
	if _, _, _, err := prepareReviewedImport(tx, actor, incarnation, input, now); err != nil {
		return "", err
	}
	if input.ID != "" {
		current, err := loadPersonalRecipe(tx, actor, incarnation, input.ID, "")
		if err != nil {
			return "", err
		}
		if current.VersionID != input.ExpectedVersion {
			fields := []string{"recipe"}
			if base, err := loadPersonalRecipe(tx, actor, incarnation, input.ID, input.ExpectedVersion); err == nil {
				fields = personalChangedRecipeFields(base, current)
			}
			return "", &PersonalVersionConflict{Current: current, Fields: fields}
		}
	} else if input.SourceRecipeID != nil {
		if _, err := personalCopySource(tx, actor.UserID, *input.SourceRecipeID); err != nil {
			return "", err
		}
		var existing personalRoot
		if err := tx.Where("user_id=? AND incarnation=? AND source_recipe_id=?", actor.UserID, incarnation, *input.SourceRecipeID).Take(&existing).Error; err == nil {
			return "", &PersonalCopyExists{ID: existing.ID}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrPersonalStorage
		}
	}
	return incarnation, nil
}

func (j *LifecycleJournal) ReservePersonalPhoto(ctx context.Context, actor PersonalActor, reader PersonalEntitlementReader, input PersonalSave, upload PersonalPhotoUpload, source string, now time.Time, admission ...PersonalPhotoAdmission) (PersonalPhotoReservation, error) {
	input, err := normalizePersonalSave(input)
	if err != nil {
		return PersonalPhotoReservation{}, err
	}
	if input.PhotoAction != "replace" || upload.Digest != input.PhotoDigest || upload.Width < 1 || upload.Height < 1 || upload.Width > 2048 || upload.Height > 2048 || upload.Size < 1 || upload.Size > 2<<20 || source == "" || len(source) > 128 || upload.Binding != "fixture" && !cloudinaryBindingPattern.MatchString(upload.Binding) {
		return PersonalPhotoReservation{}, ErrPersonalInput
	}
	hash, err := personalRequestHash(input)
	if err != nil {
		return PersonalPhotoReservation{}, err
	}
	j.cleanupMu.Lock()
	defer j.cleanupMu.Unlock()
	j.app.AccountWork.RLock()
	defer j.app.AccountWork.RUnlock()
	var out PersonalPhotoReservation
	err = j.app.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		if err := personalPermission(ctx, tx, actor, incarnation, reader, now); err != nil {
			return err
		}
		var old personalPhotoAttempt
		err = tx.Where("user_id=? AND incarnation=? AND request_key=?", actor.UserID, incarnation, input.RequestKey).Take(&old).Error
		if err == nil {
			if old.RequestHash != hash {
				return ErrPersonalKeyConflict
			}
			out.AssetID = old.AssetID
			// A completed cleanup may already have removed its work. Replays still
			// cannot create a second provider side effect.
			err := j.store.DB.WithContext(ctx).Where("id=?", old.WorkID).Take(&out.Work).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLifecycleJournal
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPersonalStorage
		}
		if _, err := personalSaveAvailable(ctx, tx, actor, reader, input, now); err != nil {
			return err
		}
		// A previously completed or deleted text/photo save owns its key too.
		if prior, err := loadPersonalReceipt(tx, actor, incarnation, input.RequestKey, hash, "save"); err != nil {
			return err
		} else if prior != nil {
			return ErrPersonalDeleted
		}
		lease := now.Add(time.Minute).UnixNano()
		if len(admission) > 0 {
			a := admission[0]
			content := input
			content.PhotoDigest = ""
			contentHash, err := personalRequestHash(content)
			if err != nil {
				return err
			}
			var prepared personalPhotoRequest
			if a.RequestKey != input.RequestKey || a.Incarnation != incarnation || !a.Dispatch || tx.Where("user_id=? AND incarnation=? AND request_key=? AND request_hash=? AND content_hash=? AND state='processing' AND lease_ns>?", actor.UserID, incarnation, input.RequestKey, a.RequestHash, contentHash, now.UnixNano()).Take(&prepared).Error != nil {
				return ErrPersonalPhotoFailed
			}
			lease = prepared.LeaseNS
			if err := tx.Model(&personalPhotoRequest{}).Where("user_id=? AND incarnation=? AND request_key=?", actor.UserID, incarnation, input.RequestKey).Update("state", "reserved").Error; err != nil {
				return ErrPersonalStorage
			}
		} else {
			if err := personalAssignmentKeyAvailable(tx, actor, incarnation, input.RequestKey); err != nil {
				return err
			}
			if err := personalPhotoSlots(tx, actor, now); err != nil {
				return err
			}
			if err := personalPhotoBudget(tx, incarnation, source, now); err != nil {
				return err
			}
		}
		assetID, err := randomLifecycleID()
		if err != nil {
			return err
		}
		workID, err := randomLifecycleID()
		if err != nil {
			return err
		}
		target, err := randomLifecycleID()
		if err != nil {
			return err
		}
		work := LifecycleWork{ID: workID, Incarnation: incarnation, Kind: "image", Target: "mychef/" + target, Provider: upload.Binding, DeliveryType: "authenticated", Purpose: "personal", State: "reserved", NextNS: lease}
		if err := j.reserveLifecycleWork(ctx, work); err != nil {
			return err
		}
		asset := PersonalPhotoAsset{ID: assetID, UserID: actor.UserID, Incarnation: incarnation, Target: work.Target, Binding: upload.Binding, DeliveryType: "authenticated", Digest: upload.Digest, Width: upload.Width, Height: upload.Height, Size: upload.Size, CreatedNS: now.UnixNano()}
		if err := tx.Create(&asset).Error; err != nil {
			return ErrPersonalStorage
		}
		attempt := personalPhotoAttempt{UserID: actor.UserID, Incarnation: incarnation, RequestKey: input.RequestKey, RequestHash: hash, AssetID: assetID, WorkID: workID, State: "reserved", LeaseNS: work.NextNS, CreatedNS: now.UnixNano()}
		if err := tx.Create(&attempt).Error; err != nil {
			return ErrPersonalStorage
		}
		out = PersonalPhotoReservation{AssetID: assetID, Work: work, Dispatch: true}
		return nil
	})
	return out, err
}

func (j *LifecycleJournal) BeginPersonalPhoto(ctx context.Context, actor PersonalActor, reader PersonalEntitlementReader, input PersonalSave, reservation PersonalPhotoReservation, now time.Time) error {
	input, err := normalizePersonalSave(input)
	if err != nil {
		return err
	}
	hash, err := personalRequestHash(input)
	if err != nil {
		return err
	}
	j.cleanupMu.Lock()
	defer j.cleanupMu.Unlock()
	j.app.AccountWork.RLock()
	defer j.app.AccountWork.RUnlock()
	return j.app.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalSaveAvailable(ctx, tx, actor, reader, input, now)
		if err != nil {
			return err
		}
		var attempt personalPhotoAttempt
		if err := tx.Where("user_id=? AND incarnation=? AND request_key=? AND request_hash=? AND asset_id=? AND work_id=? AND state='reserved' AND lease_ns>?", actor.UserID, incarnation, input.RequestKey, hash, reservation.AssetID, reservation.Work.ID, now.UnixNano()).Take(&attempt).Error; err != nil {
			return ErrPersonalPhotoFailed
		}
		if err := j.BeginImageUpload(ctx, attempt.WorkID, reservation.Work.Target); err != nil {
			return err
		}
		return tx.Model(&personalPhotoAttempt{}).Where("user_id=? AND incarnation=? AND request_key=?", actor.UserID, incarnation, input.RequestKey).Update("state", "uploading").Error
	})
}

func (j *LifecycleJournal) FinishPersonalPhoto(ctx context.Context, actor PersonalActor, reader PersonalEntitlementReader, input PersonalSave, reservation PersonalPhotoReservation, confirmed bool, now time.Time, options ...PersonalWriteOptions) (PersonalRecipe, error) {
	input, err := normalizePersonalSave(input)
	if err != nil {
		return PersonalRecipe{}, err
	}
	hash, err := personalRequestHash(input)
	if err != nil {
		return PersonalRecipe{}, err
	}
	j.cleanupMu.Lock()
	defer j.cleanupMu.Unlock()
	j.app.AccountWork.RLock()
	defer j.app.AccountWork.RUnlock()
	// This record survives a vanished/revoked account or a failed DB commit.
	if err := j.FinishImageUpload(ctx, reservation.Work.ID, confirmed); err != nil {
		return PersonalRecipe{}, err
	}
	var out PersonalRecipe
	err = j.app.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		var attempt personalPhotoAttempt
		if err := tx.Where("user_id=? AND incarnation=? AND request_key=? AND request_hash=? AND asset_id=? AND work_id=?", actor.UserID, incarnation, input.RequestKey, hash, reservation.AssetID, reservation.Work.ID).Take(&attempt).Error; err != nil {
			return ErrPersonalPhotoFailed
		}
		if !confirmed || attempt.State == "failed" || attempt.State == "reserved" || attempt.State != "attached" && attempt.LeaseNS <= now.UnixNano() {
			return ErrPersonalPhotoFailed
		}
		if len(options) > 0 && options[0].Abort {
			return ErrPersonalPhotoFailed
		}
		opts := PersonalWriteOptions{PhotoAssetID: attempt.AssetID}
		if len(options) > 0 {
			opts.Fault = options[0].Fault
		}
		out, err = savePersonalRecipeOn(ctx, tx, actor, reader, input, now, opts)
		if err != nil {
			return err
		}
		return tx.Model(&personalPhotoAttempt{}).Where("user_id=? AND incarnation=? AND request_key=?", actor.UserID, incarnation, input.RequestKey).Update("state", "attached").Error
	})
	if err != nil {
		// Do not store failed recipe bodies. The receipt plus independent target
		// retain only what is needed to refuse replay and clean up the object.
		_ = j.app.DB.WithContext(ctx).Model(&personalPhotoAttempt{}).Where("user_id=? AND request_key=? AND asset_id=? AND state!='attached'", actor.UserID, input.RequestKey, reservation.AssetID).Update("state", "failed").Error
	}
	return out, err
}
