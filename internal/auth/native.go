package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

const nativeAccessTTL = 5 * time.Minute
const nativeRefreshLimit = 10000

type NativeTokens struct {
	AccessToken        string `json:"accessToken"`
	RefreshToken       string `json:"refreshToken"`
	AccessExpiresUnix  int64  `json:"accessExpiresUnix"`
	SessionExpiresUnix int64  `json:"sessionExpiresUnix"`
}

type NativeSession struct {
	Session  store.Session
	FamilyID string
	Platform string
}

func nativePlatform(platform string) bool { return platform == "ios" || platform == "android" }

func nativeSecret(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func nativeToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}

// NativeStart creates anonymous authority only. Platform metadata grants no rights.
func (m *Manager) NativeStart(ctx context.Context, platform string) (NativeTokens, error) {
	if !nativePlatform(platform) {
		return NativeTokens{}, ErrInput
	}
	now := time.Now().UTC()
	_, anchor, err := newSession(nil, now)
	if err != nil {
		return NativeTokens{}, err
	}
	var result NativeTokens
	err = m.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("expires_unix <= ?", now.Unix()).Delete(&store.Session{}).Error; err != nil {
			return err
		}
		if err := tx.Create(&anchor).Error; err != nil {
			return err
		}
		result, err = nativeBind(tx, anchor, platform, now)
		return err
	})
	return result, err
}

// NativeBind adopts a freshly issued browser-session result inside the server.
// It never exchanges an arbitrary bearer or accepts a client-supplied account.
func (m *Manager) NativeBind(ctx context.Context, token, platform, previousFamily string) (NativeTokens, error) {
	if !nativePlatform(platform) || !nativeToken(token) {
		return NativeTokens{}, ErrInput
	}
	var result NativeTokens
	err := m.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		var anchor store.Session
		err := tx.Where(`hash=? AND expires_unix>? AND
NOT EXISTS (SELECT 1 FROM native_accesses a WHERE a.hash=sessions.hash) AND
NOT EXISTS (SELECT 1 FROM native_families f WHERE f.anchor_hash=sessions.hash)`, Hash(token), now.Unix()).Take(&anchor).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSession
		}
		if err != nil {
			return err
		}
		if previousFamily != "" {
			if err := tx.Where("id=?", previousFamily).Delete(&store.NativeFamily{}).Error; err != nil {
				return err
			}
		}
		result, err = nativeBind(tx, anchor, platform, now)
		return err
	})
	return result, err
}

func nativeBind(tx *gorm.DB, anchor store.Session, platform string, now time.Time) (NativeTokens, error) {
	id, err := nativeSecret(16)
	if err != nil {
		return NativeTokens{}, err
	}
	family := store.NativeFamily{ID: id, AnchorHash: anchor.Hash, Platform: platform, ExpiresUnix: anchor.ExpiresUnix}
	if err := tx.Create(&family).Error; err != nil {
		return NativeTokens{}, err
	}
	return nativeIssue(tx, family, anchor.UserID, now)
}

func nativeIssue(tx *gorm.DB, family store.NativeFamily, userID *int64, now time.Time) (NativeTokens, error) {
	access, err := nativeSecret(32)
	if err != nil {
		return NativeTokens{}, err
	}
	refresh, err := nativeSecret(32)
	if err != nil {
		return NativeTokens{}, err
	}
	expires := min(now.Add(nativeAccessTTL).Unix(), family.ExpiresUnix)
	// Existing sensitive transactions recheck this short-lived actor row.
	session := store.Session{Hash: Hash(access), UserID: userID, ExpiresUnix: expires}
	if err := tx.Create(&session).Error; err != nil {
		return NativeTokens{}, err
	}
	if err := tx.Create(&store.NativeAccess{Hash: session.Hash, FamilyID: family.ID}).Error; err != nil {
		return NativeTokens{}, err
	}
	if err := tx.Create(&store.NativeRefresh{Hash: Hash(refresh), FamilyID: family.ID}).Error; err != nil {
		return NativeTokens{}, err
	}
	return NativeTokens{AccessToken: access, RefreshToken: refresh, AccessExpiresUnix: expires, SessionExpiresUnix: family.ExpiresUnix}, nil
}

func (m *Manager) NativeLoad(ctx context.Context, token string) (NativeSession, error) {
	if !nativeToken(token) {
		return NativeSession{}, ErrSession
	}
	var row struct {
		store.Session
		FamilyID string
		Platform string
	}
	err := m.Store.DB.WithContext(ctx).Table("sessions s").Select("s.hash,s.user_id,s.expires_unix,a.family_id,f.platform").
		Joins("JOIN native_accesses a ON a.hash=s.hash").
		Joins("JOIN native_families f ON f.id=a.family_id").
		Joins("JOIN sessions root ON root.hash=f.anchor_hash").
		Where("s.hash=? AND s.expires_unix>unixepoch() AND f.expires_unix>unixepoch() AND root.expires_unix>unixepoch() AND s.user_id IS root.user_id", Hash(token)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return NativeSession{}, ErrSession
	}
	if err != nil {
		return NativeSession{}, err
	}
	return NativeSession{Session: row.Session, FamilyID: row.FamilyID, Platform: row.Platform}, nil
}

func (m *Manager) NativeRefresh(ctx context.Context, token string) (NativeTokens, error) {
	if !nativeToken(token) {
		return NativeTokens{}, ErrSession
	}
	var result NativeTokens
	rejected := false
	err := m.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		var credential store.NativeRefresh
		err := tx.Where("hash=?", Hash(token)).Take(&credential).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			rejected = true
			return nil
		}
		if err != nil {
			return err
		}
		var family store.NativeFamily
		if err := tx.Where("id=?", credential.FamilyID).Take(&family).Error; err != nil {
			return err
		}
		var anchor store.Session
		if err := tx.Where("hash=?", family.AnchorHash).Take(&anchor).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&store.NativeRefresh{}).Where("family_id=?", family.ID).Count(&count).Error; err != nil {
			return err
		}
		if credential.ConsumedUnix != nil || family.ExpiresUnix <= now.Unix() || anchor.ExpiresUnix <= now.Unix() || count >= nativeRefreshLimit {
			rejected = true
			// Commit revocation before returning ErrSession; returning it here
			// would roll back the security action along with the transaction.
			return tx.Delete(&family).Error
		}
		if err := tx.Model(&credential).Update("consumed_unix", now.Unix()).Error; err != nil {
			return err
		}
		if err := tx.Where("family_id=?", family.ID).Delete(&store.NativeAccess{}).Error; err != nil {
			return err
		}
		result, err = nativeIssue(tx, family, anchor.UserID, now)
		return err
	})
	if err != nil {
		return NativeTokens{}, err
	}
	if rejected {
		return NativeTokens{}, ErrSession
	}
	return result, nil
}

func (m *Manager) NativeRevoke(ctx context.Context, familyID string) error {
	return m.Store.DB.WithContext(ctx).Where("id=?", familyID).Delete(&store.NativeFamily{}).Error
}
