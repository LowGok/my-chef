package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

func (s *Store) PersonalCapability(ctx context.Context, actor PersonalActor, reader PersonalEntitlementReader, now time.Time) (bool, error) {
	allowed := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		err = personalPermission(ctx, tx, actor, incarnation, reader, now)
		if errors.Is(err, ErrPersonalPermission) {
			return nil
		}
		if err != nil {
			return err
		}
		allowed = true
		return nil
	})
	return allowed, err
}
