package auth

import (
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func requireAppUser(tx *gorm.DB, userID *int64) error {
	if userID == nil {
		return nil
	}
	var count int64
	if err := tx.Model(&store.User{}).Where("user_id=? AND isNewsletterOnly=0", *userID).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrSession
	}
	return nil
}
