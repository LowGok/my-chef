package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

var (
	ErrCurrentPassword = errors.New("incorrect current password")
	ErrPasswordReuse   = errors.New("current password reused")
)

func (m *Manager) ChangePassword(ctx context.Context, userID int64, token, current, proposed string) (store.User, string, error) {
	if len(token) != 64 {
		return store.User{}, "", ErrSession
	}
	if len(current) > MaxPasswordBytes || len(proposed) > MaxPasswordBytes || !utf8.ValidString(current) || !utf8.ValidString(proposed) {
		return store.User{}, "", ErrInput
	}
	normalized, err := normalizePassword(current)
	if err != nil {
		return store.User{}, "", ErrCurrentPassword
	}
	var user store.User
	if err := m.Store.DB.WithContext(ctx).First(&user, userID).Error; err != nil {
		return store.User{}, "", err
	}
	matched, _, err := comparePassword(ctx, user.Password, current, normalized)
	if err != nil {
		return store.User{}, "", err
	}
	if !matched {
		return store.User{}, "", ErrCurrentPassword
	}
	next, err := normalizePassword(proposed)
	if err != nil {
		return store.User{}, "", err
	}
	if next == normalized {
		return store.User{}, "", ErrPasswordReuse
	}
	if _, err := checkNewPassword(proposed); err != nil {
		return store.User{}, "", err
	}
	hash, err := newPasswordHash(ctx, next)
	if err != nil {
		return store.User{}, "", err
	}
	return m.commitPasswordChange(ctx, user, token, hash)
}

func (m *Manager) commitPasswordChange(ctx context.Context, verified store.User, initiatingToken, replacementHash string) (store.User, string, error) {
	var key [24]byte
	if _, err := rand.Read(key[:]); err != nil {
		return store.User{}, "", err
	}
	var user store.User
	var token string
	err := m.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		var initiating store.Session
		err := tx.Where("hash = ? AND user_id = ? AND expires_unix > ?", Hash(initiatingToken), verified.ID, now.Unix()).Take(&initiating).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSession
		}
		if err != nil {
			return err
		}
		result := tx.Model(&store.User{}).
			Where("user_id = ? AND password = ?", verified.ID, verified.Password).
			Update("password", replacementHash)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrSession
		}
		if err := tx.First(&user, verified.ID).Error; err != nil {
			return err
		}
		if err := invalidateRecovery(ctx, tx, now, user.ID, m.RecoveryRateKey, user.EmailKey); err != nil {
			return err
		}
		if err := cancelEmailChanges(ctx, tx, now, user.ID); err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&store.Session{}).Error; err != nil {
			return err
		}
		value, replacement, err := newSession(&user.ID, now)
		if err != nil {
			return err
		}
		if err := tx.Create(&replacement).Error; err != nil {
			return err
		}
		if _, err := mailqueue.Enqueue(ctx, m.Store, tx, now, mailqueue.Request{
			OwnerID: user.ID, Key: "password-changed/" + hex.EncodeToString(key[:]),
			Recipient: user.Email,
			Template:  mailqueue.PasswordChangedTemplate,
			Fields:    map[string]string{"timestamp": now.Format(time.RFC3339)},
		}); err != nil {
			return err
		}
		token = value
		return nil
	})
	if err != nil {
		return store.User{}, "", err
	}
	return user, token, nil
}
