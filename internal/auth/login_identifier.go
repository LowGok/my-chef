package auth

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

// LoginTarget is an authority snapshot for one sign-in attempt. The HTTP
// handler uses its bucket before doing password work on that same snapshot.
type LoginTarget struct {
	user        store.User
	usernameKey []byte
	bucket      string
	eligible    bool
}

// RateLimitKey makes known aliases share a bucket without leaking the aliases
// or allowing unresolved input to select a known account's bucket.
func (t LoginTarget) RateLimitKey() string {
	if t.user.ID != 0 {
		return "account:" + Hash("user:"+strconv.FormatInt(t.user.ID, 10))
	}
	return "account:" + Hash("unresolved:"+t.bucket)
}

// ResolveLogin preserves the existing email namespace and resolves at most one
// account. A legacy username that resembles email never receives a fallback.
func (m *Manager) ResolveLogin(ctx context.Context, input string) (LoginTarget, error) {
	input = strings.TrimSpace(input)
	target := LoginTarget{bucket: "invalid:" + input}
	db := m.Store.DB.WithContext(ctx)
	if strings.ContainsRune(input, '@') {
		_, key, err := store.EmailIdentity(input)
		if err != nil {
			return target, nil
		}
		target.bucket, target.eligible = "email:"+key, true
		err = db.Where("email_key = ? AND isNewsletterOnly=0", key).Take(&target.user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = nil
		}
		return target, err
	}
	key := store.UsernameKey(input)
	if key == nil || strings.ContainsRune(input, 0) {
		return target, nil
	}
	target.usernameKey, target.bucket, target.eligible = key, "username:"+string(key), true
	var err error
	target.user, err = lookupLoginUsername(db, key)
	return target, err
}

func lookupLoginUsername(db *gorm.DB, key []byte) (store.User, error) {
	var rows []store.User
	if err := db.Where("isNewsletterOnly=0 AND login_username_key=?", key).Limit(2).Find(&rows).Error; err != nil {
		return store.User{}, err
	}
	if len(rows) != 1 || !bytes.Equal(store.UsernameKey(rows[0].Username), key) {
		return store.User{}, nil
	}
	return rows[0], nil
}

// LoginResolvedSession verifies and commits exactly the resolved candidate,
// with no second selection after the caller charges its attempt.
func (m *Manager) LoginResolvedSession(ctx context.Context, target LoginTarget, password, old string) (store.User, string, error) {
	login, err := verifyResolvedLogin(ctx, target, password)
	if err != nil {
		return store.User{}, "", err
	}
	return m.completeLogin(ctx, login, old, true)
}

func verifyResolvedLogin(ctx context.Context, target LoginTarget, raw string) (verifiedLogin, error) {
	normalized, err := normalizePassword(raw)
	if err != nil || !target.eligible {
		return verifiedLogin{}, ErrCredentials
	}
	u := target.user
	matched, legacy, err := comparePassword(ctx, u.Password, raw, normalized)
	if err != nil {
		return verifiedLogin{}, err
	}
	if !matched || u.ID == 0 {
		return verifiedLogin{}, ErrCredentials
	}
	result := verifiedLogin{user: u, usernameKey: target.usernameKey}
	if legacy {
		result.upgrade, err = newPasswordHash(ctx, normalized)
	}
	return result, err
}
