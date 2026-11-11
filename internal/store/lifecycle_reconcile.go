package store

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// ReconcileAccountDeletions runs before any background account work or HTTP
// readiness. Markers are authoritative across application snapshot rollback.
func (j *LifecycleJournal) ReconcileAccountDeletions(ctx context.Context, opts AccountDeletionOptions) error {
	if err := j.CheckAvailable(ctx); err != nil {
		return err
	}
	j.app.AccountWork.Lock()
	defer j.app.AccountWork.Unlock()
	if err := j.ReconcileIntents(ctx); err != nil {
		return err
	}
	after := ""
	for {
		var markers []lifecycleDeletion
		if err := j.store.DB.WithContext(ctx).Where("state='committed' AND operation>?", after).Order("operation").Limit(128).Find(&markers).Error; err != nil {
			return ErrLifecycleJournal
		}
		if len(markers) == 0 {
			return nil
		}
		for _, marker := range markers {
			if err := j.purgeRestoredAccount(ctx, marker, opts); err != nil {
				return err
			}
			j.CancelAccountWork(marker.Incarnation)
			after = marker.Operation
		}
	}
}
func (j *LifecycleJournal) purgeRestoredAccount(ctx context.Context, marker lifecycleDeletion, opts AccountDeletionOptions) error {
	return j.app.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user User
		err := tx.Table("users AS u").Select("u.*").Joins("JOIN account_incarnations a ON a.user_id=u.user_id").Where("a.incarnation=?", marker.Incarnation).Take(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return ErrLifecycleJournal
		}
		if err := j.captureStoredAssets(ctx, tx, user.ID, marker.Incarnation); err != nil {
			return ErrLifecycleJournal
		}
		if opts.Capture != nil {
			if err := opts.Capture(tx, user, marker.Incarnation); err != nil {
				return err
			}
		}
		if opts.Cleanup != nil {
			if err := opts.Cleanup(tx, user); err != nil {
				return err
			}
		}
		if err := purgeAccountRows(tx, user.ID, marker.Incarnation, AccountDeletionOptions{}); err != nil {
			return ErrLifecycleJournal
		}
		if err := tx.Where("user_id=?", user.ID).Delete(&User{}).Error; err != nil {
			return ErrLifecycleJournal
		}
		if err := tx.Exec("INSERT INTO lifecycle_operations(operation,incarnation,created_ns) VALUES(?,?,?) ON CONFLICT DO NOTHING", marker.Operation, marker.Incarnation, marker.CreatedNS).Error; err != nil {
			return ErrLifecycleJournal
		}
		var operation string
		if err := tx.Raw("SELECT operation FROM lifecycle_operations WHERE incarnation=?", marker.Incarnation).Row().Scan(&operation); err != nil || operation != marker.Operation {
			return ErrLifecycleJournal
		}
		return nil
	})
}
