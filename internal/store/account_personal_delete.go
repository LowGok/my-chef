package store

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

func (j *LifecycleJournal) capturePersonalAssets(ctx context.Context, tx *gorm.DB, user int64, incarnation string) error {
	rows, err := tx.Raw("SELECT target,binding,delivery_type FROM personal_photo_assets WHERE user_id=? AND incarnation=? ORDER BY id", user, incarnation).Rows()
	if err != nil {
		return ErrLifecycleJournal
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var target, binding, delivery string
		if err := rows.Scan(&target, &binding, &delivery); err != nil {
			return ErrLifecycleJournal
		}
		if !validPersonalTarget(target) || delivery != "authenticated" || binding != "fixture" && !cloudinaryBindingPattern.MatchString(binding) {
			return ErrLifecycleJournal
		}
		var old LifecycleWork
		err := j.store.DB.WithContext(ctx).Where("kind='image' AND target=? AND provider=?", target, binding).Take(&old).Error
		if err == nil {
			if old.Incarnation != incarnation || old.DeliveryType != delivery || old.Purpose != "personal" {
				return ErrLifecycleJournal
			}
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrLifecycleJournal
		}
		id, err := randomLifecycleID()
		if err != nil {
			return err
		}
		work := LifecycleWork{ID: id, Incarnation: incarnation, Kind: "image", Target: target, Provider: binding, DeliveryType: delivery, Purpose: "personal", State: "live", StartedNS: 1, Finished: true, Confirmed: true}
		if err := j.reserveLifecycleWork(ctx, work, true); err != nil {
			return err
		}
	}
	if rows.Err() != nil {
		return ErrLifecycleJournal
	}
	return nil
}
