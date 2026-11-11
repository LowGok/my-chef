package auth

import (
	"context"
	"errors"

	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

// VerifyAccountAction binds password verification to one current credential and
// session. The store rechecks both inside the export or deletion transaction.
func (m *Manager) VerifyAccountAction(ctx context.Context, userID int64, token, current string) (store.AccountActor, error) {
	session, err := m.Load(ctx, token)
	if err != nil {
		return store.AccountActor{}, err
	}
	if session.UserID == nil || *session.UserID != userID {
		return store.AccountActor{}, ErrSession
	}
	normalized, err := normalizePassword(current)
	if err != nil {
		return store.AccountActor{}, ErrCurrentPassword
	}
	var user store.User
	if err := m.Store.DB.WithContext(ctx).First(&user, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return store.AccountActor{}, ErrSession
		}
		return store.AccountActor{}, err
	}
	matched, _, err := comparePassword(ctx, user.Password, current, normalized)
	if err != nil {
		return store.AccountActor{}, err
	}
	if !matched {
		return store.AccountActor{}, ErrCurrentPassword
	}
	return store.AccountActor{UserID: user.ID, SessionHash: Hash(token), PasswordHash: user.Password}, nil
}
