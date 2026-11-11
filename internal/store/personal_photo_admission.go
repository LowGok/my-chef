package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

func unusedPersonalPhotoKey(tx *gorm.DB, actor PersonalActor, incarnation, key string) error {
	var count int
	if err := tx.Raw(`SELECT (SELECT COUNT(*) FROM personal_photo_requests WHERE user_id=? AND incarnation=? AND request_key=?) + (SELECT COUNT(*) FROM personal_photo_attempts WHERE user_id=? AND incarnation=? AND request_key=?)`, actor.UserID, incarnation, key, actor.UserID, incarnation, key).Row().Scan(&count); err != nil {
		return ErrPersonalStorage
	}
	if count != 0 {
		return ErrPersonalKeyConflict
	}
	return nil
}

func personalPhotoSlots(tx *gorm.DB, actor PersonalActor, now time.Time) error {
	var global, account int
	query := `SELECT COUNT(*),COALESCE(SUM(CASE WHEN user_id=? THEN 1 ELSE 0 END),0) FROM (
SELECT user_id FROM personal_photo_attempts WHERE state IN ('reserved','uploading') AND lease_ns>?
UNION ALL SELECT user_id FROM personal_photo_requests WHERE state='processing' AND lease_ns>?)`
	if err := tx.Raw(query, actor.UserID, now.UnixNano(), now.UnixNano()).Row().Scan(&global, &account); err != nil {
		return ErrPersonalStorage
	}
	if account >= 1 || global >= 2 {
		return ErrPersonalPhotoBusy
	}
	return nil
}

// Admission precedes decoding. Invalid files consume the same hourly budget,
// and at most two bounded decoder/upload requests run at once across handles.
func (j *LifecycleJournal) AdmitPersonalPhoto(ctx context.Context, actor PersonalActor, reader PersonalEntitlementReader, input PersonalSave, source string, now time.Time) (PersonalPhotoAdmission, error) {
	input, err := normalizePersonalSave(input)
	if err != nil {
		return PersonalPhotoAdmission{}, err
	}
	if input.PhotoAction != "replace" || source == "" || len(source) > 128 {
		return PersonalPhotoAdmission{}, ErrPersonalInput
	}
	hash, err := personalRequestHash(input)
	if err != nil {
		return PersonalPhotoAdmission{}, err
	}
	content := input
	content.PhotoDigest = ""
	contentHash, err := personalRequestHash(content)
	if err != nil {
		return PersonalPhotoAdmission{}, err
	}
	j.app.AccountWork.RLock()
	defer j.app.AccountWork.RUnlock()
	var out PersonalPhotoAdmission
	err = j.app.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		if err := personalPermission(ctx, tx, actor, incarnation, reader, now); err != nil {
			return err
		}
		var prior personalPhotoRequest
		err = tx.Where("user_id=? AND incarnation=? AND request_key=?", actor.UserID, incarnation, input.RequestKey).Take(&prior).Error
		if err == nil {
			if prior.RequestHash != hash {
				return ErrPersonalKeyConflict
			}
			out = PersonalPhotoAdmission{RequestKey: input.RequestKey, RequestHash: hash, Incarnation: incarnation}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPersonalStorage
		}
		if _, err := personalSaveAvailable(ctx, tx, actor, reader, input, now); err != nil {
			return err
		}
		var used int64
		if err := tx.Model(&personalOperation{}).Where("user_id=? AND incarnation=? AND request_key=?", actor.UserID, incarnation, input.RequestKey).Count(&used).Error; err != nil {
			return ErrPersonalStorage
		}
		if used != 0 {
			return ErrPersonalKeyConflict
		}
		if err := personalAssignmentKeyAvailable(tx, actor, incarnation, input.RequestKey); err != nil {
			return err
		}
		if err := personalPhotoSlots(tx, actor, now); err != nil {
			return err
		}
		if err := personalPhotoBudget(tx, incarnation, source, now); err != nil {
			return err
		}
		row := personalPhotoRequest{UserID: actor.UserID, Incarnation: incarnation, RequestKey: input.RequestKey, RequestHash: hash, ContentHash: contentHash, State: "processing", LeaseNS: now.Add(time.Minute).UnixNano()}
		if err := tx.Create(&row).Error; err != nil {
			return ErrPersonalStorage
		}
		out = PersonalPhotoAdmission{RequestKey: input.RequestKey, RequestHash: hash, Incarnation: incarnation, Dispatch: true}
		return nil
	})
	return out, err
}

func (j *LifecycleJournal) FailPersonalPhotoAdmission(ctx context.Context, actor PersonalActor, admission PersonalPhotoAdmission) error {
	return j.app.DB.WithContext(ctx).Model(&personalPhotoRequest{}).Where("user_id=? AND incarnation=? AND request_key=? AND request_hash=? AND state='processing'", actor.UserID, admission.Incarnation, admission.RequestKey, admission.RequestHash).Update("state", "failed").Error
}
