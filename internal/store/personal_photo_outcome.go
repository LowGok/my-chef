package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

func (s *Store) PersonalPhotoOutcome(ctx context.Context, actor PersonalActor, admission PersonalPhotoAdmission, now time.Time) (PersonalRecipe, error) {
	var out PersonalRecipe
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		if incarnation != admission.Incarnation {
			return ErrPersonalPhotoFailed
		}
		var request personalPhotoRequest
		if err := tx.Where("user_id=? AND incarnation=? AND request_key=? AND request_hash=?", actor.UserID, incarnation, admission.RequestKey, admission.RequestHash).Take(&request).Error; err != nil {
			return ErrPersonalPhotoFailed
		}
		var receipt personalOperation
		var attempt personalPhotoAttempt
		attemptErr := tx.Where("user_id=? AND incarnation=? AND request_key=?", actor.UserID, incarnation, admission.RequestKey).Take(&attempt).Error
		if attemptErr != nil && !errors.Is(attemptErr, gorm.ErrRecordNotFound) {
			return ErrPersonalStorage
		}
		err = tx.Where("user_id=? AND incarnation=? AND request_key=? AND action='save'", actor.UserID, incarnation, admission.RequestKey).Take(&receipt).Error
		if err == nil {
			if attemptErr != nil || receipt.RequestHash != attempt.RequestHash || attempt.State != "attached" {
				return ErrPersonalPhotoFailed
			}
			if receipt.State == "deleted" {
				return ErrPersonalDeleted
			}
			out, err = loadPersonalRecipe(tx, actor, incarnation, receipt.RecipeID, receipt.VersionID)
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPersonalStorage
		}
		if attemptErr == nil && attempt.State == "failed" || request.State == "failed" || request.LeaseNS <= now.UnixNano() {
			return ErrPersonalPhotoFailed
		}
		return ErrPersonalPhotoBusy
	})
	return out, err
}
