package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/store"
	"github.com/ncruces/go-sqlite3"
	"gorm.io/gorm"
)

var ErrCredentials = errors.New("invalid credentials")
var ErrInput = errors.New("invalid input")
var ErrConflict = errors.New("account details unavailable")
var ErrSession = errors.New("invalid session")

type Manager struct {
	Store           *store.Store
	RecoveryRateKey []byte
}

func New(s *store.Store) *Manager { return &Manager{Store: s} }

func Hash(token string) string { h := sha256.Sum256([]byte(token)); return hex.EncodeToString(h[:]) }
func CSRF(token string) string { return Hash("mychef:csrf:" + token) }
func ValidCSRF(token, value string) bool {
	return len(token) == 64 && subtle.ConstantTimeCompare([]byte(CSRF(token)), []byte(value)) == 1
}
func (m *Manager) Load(ctx context.Context, token string) (store.Session, error) {
	if len(token) != 64 {
		return store.Session{}, ErrSession
	}
	var session store.Session
	err := m.Store.DB.WithContext(ctx).Where(`hash = ? AND expires_unix > ?
AND NOT EXISTS (SELECT 1 FROM native_accesses n WHERE n.hash=sessions.hash)
AND NOT EXISTS (SELECT 1 FROM native_families f WHERE f.anchor_hash=sessions.hash)`, Hash(token), time.Now().UTC().Unix()).First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return session, ErrSession
	}
	return session, err
}
func newSession(userID *int64, now time.Time) (string, store.Session, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", store.Session{}, err
	}
	token := hex.EncodeToString(value[:])
	ttl := time.Hour
	if userID != nil {
		ttl = 7 * 24 * time.Hour
	}
	return token, store.Session{Hash: Hash(token), UserID: userID, ExpiresUnix: now.Add(ttl).Unix()}, nil
}

func rotateSession(tx *gorm.DB, session store.Session, old string, now int64) error {
	if len(old) == 64 {
		if err := tx.Where("hash = ?", Hash(old)).Delete(&store.Session{}).Error; err != nil {
			return err
		}
	}
	if err := tx.Where("expires_unix <= ?", now).Delete(&store.Session{}).Error; err != nil {
		return err
	}
	return tx.Create(&session).Error
}

func (m *Manager) Rotate(ctx context.Context, userID *int64, old string) (string, error) {
	now := time.Now().UTC()
	token, session, err := newSession(userID, now)
	if err != nil {
		return "", err
	}
	err = m.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return rotateSession(tx, session, old, now.Unix())
	})
	if err != nil {
		return "", err
	}
	return token, nil
}
func (m *Manager) Revoke(ctx context.Context, token string) error {
	return m.Store.DB.WithContext(ctx).Where("hash = ?", Hash(token)).Delete(&store.Session{}).Error
}

type Registration struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	Username  string `json:"username"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

func ValidName(v string, max int) bool {
	return strings.TrimSpace(v) == v && len(v) > 0 && len(v) <= max
}
func (m *Manager) Register(ctx context.Context, r Registration) (store.User, error) {
	address, err := mail.ParseAddress(r.Email)
	if err != nil || address.Address != r.Email || len(r.Email) > 254 || !ValidName(r.Username, 80) || !ValidName(r.FirstName, 100) || !ValidName(r.LastName, 100) {
		return store.User{}, ErrInput
	}
	normalized, err := checkNewPassword(r.Password)
	if err != nil {
		return store.User{}, err
	}
	hash, err := newPasswordHash(ctx, normalized)
	if err != nil {
		return store.User{}, err
	}
	prefs, _ := json.Marshal(domain.EmptyPreferences())
	user := store.User{Email: r.Email, Password: hash, Username: r.Username, FirstName: r.FirstName, LastName: r.LastName, PreferencesJSON: store.Ptr(string(prefs))}
	if err = m.Store.DB.WithContext(ctx).Create(&user).Error; errors.Is(err, sqlite3.CONSTRAINT_UNIQUE) {
		return store.User{}, ErrConflict
	}
	return user, err
}

type verifiedLogin struct {
	user    store.User
	upgrade string
}

func (m *Manager) verifyLogin(ctx context.Context, address, raw string) (verifiedLogin, error) {
	normalized, err := normalizePassword(raw)
	_, emailKey, emailErr := store.EmailIdentity(address)
	if err != nil || emailErr != nil {
		return verifiedLogin{}, ErrCredentials
	}
	var u store.User
	err = m.Store.DB.WithContext(ctx).Where("email_key = ?", emailKey).First(&u).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return verifiedLogin{}, err
	}
	matched, legacy, err := comparePassword(ctx, u.Password, raw, normalized)
	if err != nil {
		return verifiedLogin{}, err
	}
	if !matched || u.ID == 0 {
		return verifiedLogin{}, ErrCredentials
	}
	login := verifiedLogin{user: u}
	if legacy {
		login.upgrade, err = newPasswordHash(ctx, normalized)
	}
	return login, err
}

// completeLogin binds session creation and any legacy upgrade to the same
// credential version that was checked outside the write transaction.
func (m *Manager) completeLogin(ctx context.Context, login verifiedLogin, old string, withSession bool) (store.User, string, error) {
	now := time.Now().UTC()
	var token string
	var session store.Session
	if withSession {
		var err error
		token, session, err = newSession(&login.user.ID, now)
		if err != nil {
			return store.User{}, "", err
		}
	}
	var current store.User
	err := m.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("user_id = ? AND password = ? AND email_key = ?", login.user.ID, login.user.Password, login.user.EmailKey).
			Take(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCredentials
		}
		if err != nil {
			return err
		}
		if login.upgrade != "" {
			result := tx.Model(&store.User{}).Where("user_id = ? AND password = ?", current.ID, current.Password).Update("password", login.upgrade)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrCredentials
			}
			current.Password = login.upgrade
		}
		if withSession {
			var previous store.Session
			err = tx.Where("hash = ? AND expires_unix > ?", Hash(old), now.Unix()).Take(&previous).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSession
			}
			if err != nil {
				return err
			}
			return rotateSession(tx, session, old, now.Unix())
		}
		return nil
	})
	if err != nil {
		return store.User{}, "", err
	}
	return current, token, nil
}

func (m *Manager) Login(ctx context.Context, email, password string) (store.User, error) {
	login, err := m.verifyLogin(ctx, email, password)
	if err != nil {
		return store.User{}, err
	}
	u, _, err := m.completeLogin(ctx, login, "", false)
	return u, err
}

func (m *Manager) LoginSession(ctx context.Context, email, password, old string) (store.User, string, error) {
	login, err := m.verifyLogin(ctx, email, password)
	if err != nil {
		return store.User{}, "", err
	}
	return m.completeLogin(ctx, login, old, true)
}

func (m *Manager) RegistrationSession(ctx context.Context, user store.User, old string) (store.User, string, error) {
	return m.completeLogin(ctx, verifiedLogin{user: user}, old, true)
}

type ProfileUpdate struct {
	Username    string             `json:"username"`
	FirstName   string             `json:"firstName"`
	LastName    string             `json:"lastName"`
	Birthdate   *string            `json:"birthdate"`
	Sex         *string            `json:"sex"`
	Preferences domain.Preferences `json:"preferences"`
}

func (m *Manager) Update(ctx context.Context, id int64, p ProfileUpdate, tokens ...string) (store.Profile, error) {
	if len(tokens) > 1 || len(tokens) == 1 && !nativeToken(tokens[0]) {
		return store.Profile{}, ErrSession
	}
	if !ValidName(p.Username, 80) || !ValidName(p.FirstName, 100) || !ValidName(p.LastName, 100) || p.Sex != nil && len(*p.Sex) > 80 || len(p.Preferences.Effort) > 120 {
		return store.Profile{}, ErrInput
	}
	if p.Birthdate != nil {
		d, e := time.Parse(time.DateOnly, *p.Birthdate)
		if e != nil || d.After(time.Now().UTC()) {
			return store.Profile{}, ErrInput
		}
	}
	for _, list := range [][]string{p.Preferences.Diet, p.Preferences.Allergens, p.Preferences.Dislikes, p.Preferences.Technique} {
		if list == nil || len(list) > 30 {
			return store.Profile{}, ErrInput
		}
		for _, v := range list {
			if !ValidName(v, 200) {
				return store.Profile{}, ErrInput
			}
		}
	}
	prefs, _ := json.Marshal(p.Preferences)
	query := m.Store.DB.WithContext(ctx).Model(&store.User{}).Where("user_id = ?", id)
	if len(tokens) == 1 {
		query = query.Where("EXISTS (SELECT 1 FROM sessions s WHERE s.user_id=users.user_id AND s.hash=? AND s.expires_unix>unixepoch())", Hash(tokens[0]))
	}
	result := query.Updates(map[string]any{"firstname": p.FirstName, "lastname": p.LastName, "username": p.Username, "birthdate": p.Birthdate, "sex": p.Sex, "preferences_json": string(prefs), "haspref": true})
	if errors.Is(result.Error, sqlite3.CONSTRAINT_UNIQUE) {
		return store.Profile{}, ErrConflict
	}
	if result.Error != nil {
		return store.Profile{}, result.Error
	}
	if result.RowsAffected != 1 {
		if len(tokens) == 1 {
			return store.Profile{}, ErrSession
		}
		return store.Profile{}, gorm.ErrRecordNotFound
	}
	var user store.User
	err := m.Store.DB.WithContext(ctx).First(&user, id).Error
	if err != nil {
		return store.Profile{}, err
	}
	return m.Store.AccountProfile(ctx, user)
}
