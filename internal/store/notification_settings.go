package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/LowGok/my-chef/migrations"
	"gorm.io/gorm"
)

var (
	ErrReminderInput      = errors.New("invalid reminder settings")
	ErrReminderPermission = errors.New("reminder permission required")
	ErrReminderMissing    = errors.New("reminder unavailable")
)

type ReminderPreferences struct {
	Zone          string `json:"zone"`
	Current       bool   `json:"current"`
	Advance       bool   `json:"advance"`
	AdvanceDay    int    `json:"advanceDay"`
	AdvanceMinute int    `json:"advanceMinute"`
	Revision      int64  `json:"revision"`
	Premium       bool   `json:"premium"`
	DeviceEnabled bool   `json:"deviceEnabled"`
}

type ReminderSettings struct {
	store        *Store
	key          []byte
	vault        cipher.AEAD
	read         PersonalEntitlementReader
	now          func() time.Time
	supports     func(string) bool
	zoneFailures atomic.Int64
}

func NewReminderSettings(s *Store, key []byte, reader PersonalEntitlementReader, clock func() time.Time) (*ReminderSettings, error) {
	if s == nil || len(key) != 32 || reader == nil {
		return nil, ErrReminderInput
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	vault, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	if clock == nil {
		clock = time.Now
	}
	return &ReminderSettings{store: s, key: append([]byte(nil), key...), vault: vault, read: reader, now: clock}, nil
}

// SetProviderAvailability configures immutable runtime provider availability before serving.
func (s *ReminderSettings) SetProviderAvailability(supports func(string) bool) {
	s.supports = supports
}

func (s *ReminderSettings) providerEnabled(provider string) bool {
	return s.supports == nil || s.supports(provider)
}

func reminderSchemaChecksum() string {
	sum := sha256.Sum256([]byte(nativeSchemaSQL() + migrations.EntitlementGrants + migrations.WeeklyReminders))
	return hex.EncodeToString(sum[:])
}

func (s *Store) migrateWeeklyReminders(ctx context.Context) error {
	tx, err := s.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var checksum string
	var dirty bool
	if err := tx.QueryRowContext(ctx, "SELECT version,checksum,dirty FROM schema_migrations").Scan(&version, &checksum, &dirty); err != nil {
		return err
	}
	if version != 18 || checksum != entitlementSchemaChecksum() || dirty {
		return errors.New("unexpected schema before weekly reminder migration")
	}
	if _, err := tx.ExecContext(ctx, migrations.WeeklyReminders); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_migrations SET version=19,checksum=?,dirty=0 WHERE version=18", reminderSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

var zoneShape = regexp.MustCompile(`^(?:UTC|[A-Za-z0-9_+-]+(?:/[A-Za-z0-9_+-]+)+)$`)
var tokenShape = regexp.MustCompile(`^[A-Za-z0-9_:-]{32,512}$`)
var apnsTokenShape = regexp.MustCompile(`^(?:[0-9a-f]{2}){16,256}$`)

func validReminderPreferences(p ReminderPreferences) bool {
	if len(p.Zone) > 100 || !zoneShape.MatchString(p.Zone) || p.AdvanceDay < 0 || p.AdvanceDay > 6 || p.AdvanceMinute < 0 || p.AdvanceMinute >= 1440 {
		return false
	}
	_, err := time.LoadLocation(p.Zone)
	return err == nil
}

func loadReminderPreferences(tx *gorm.DB, incarnation string) (ReminderPreferences, error) {
	var p ReminderPreferences
	err := tx.Raw(`SELECT zone,current_enabled,advance_enabled,advance_day,advance_minute,revision
FROM reminder_preferences WHERE incarnation=?`, incarnation).Row().Scan(&p.Zone, &p.Current, &p.Advance, &p.AdvanceDay, &p.AdvanceMinute, &p.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return ReminderPreferences{}, nil
	}
	return p, err
}

func (s *ReminderSettings) reminderPremium(ctx context.Context, tx *gorm.DB, user int64, incarnation string, at time.Time) (bool, error) {
	value, err := s.read(ctx, tx, user, incarnation)
	return value.Premium && value.ExpiresUnix > at.Unix(), err
}

func (s *ReminderSettings) Preferences(ctx context.Context, actor PersonalActor, families ...string) (ReminderPreferences, error) {
	var p ReminderPreferences
	err := s.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		at := s.now()
		owner, err := personalAuthority(tx, actor, at)
		if err != nil {
			return err
		}
		p, err = loadReminderPreferences(tx, owner)
		if err != nil {
			return err
		}
		p.Premium, err = s.reminderPremium(ctx, tx, actor.UserID, owner, at)
		if err != nil {
			return err
		}
		p.Advance = p.Advance && p.Premium
		if !p.Premium {
			p.AdvanceDay, p.AdvanceMinute = 0, 0
		}
		if len(families) != 0 && families[0] != "" {
			return tx.Raw("SELECT EXISTS(SELECT 1 FROM reminder_installations WHERE family_id=? AND incarnation=?)", families[0], owner).Row().Scan(&p.DeviceEnabled)
		}
		return nil
	})
	return p, err
}

func (s *ReminderSettings) SetPreferences(ctx context.Context, actor PersonalActor, p ReminderPreferences) (ReminderPreferences, error) {
	if !validReminderPreferences(p) {
		return ReminderPreferences{}, ErrReminderInput
	}
	err := s.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		at := s.now()
		owner, err := personalAuthority(tx, actor, at)
		if err != nil {
			return err
		}
		p.Premium, err = s.reminderPremium(ctx, tx, actor.UserID, owner, at)
		if err != nil {
			return err
		}
		if !p.Premium && (p.Advance || p.AdvanceDay != 0 || p.AdvanceMinute != 0) {
			return ErrReminderPermission
		}
		previous, err := loadReminderPreferences(tx, owner)
		if err != nil {
			return err
		}
		if !p.Premium {
			p.AdvanceDay, p.AdvanceMinute = previous.AdvanceDay, previous.AdvanceMinute
		}
		if previous.Revision != 0 && p.Zone == previous.Zone && p.Current == previous.Current && p.Advance == previous.Advance &&
			p.AdvanceDay == previous.AdvanceDay && p.AdvanceMinute == previous.AdvanceMinute {
			p.Revision = previous.Revision
			return nil
		}
		p.DeviceEnabled = false
		err = tx.Raw(`INSERT INTO reminder_preferences(incarnation,zone,current_enabled,advance_enabled,advance_day,advance_minute,revision,next_evaluation)
VALUES(?,?,?,?,?,?,1,?) ON CONFLICT(incarnation) DO UPDATE SET zone=excluded.zone,current_enabled=excluded.current_enabled,
advance_enabled=excluded.advance_enabled,advance_day=excluded.advance_day,advance_minute=excluded.advance_minute,
revision=reminder_preferences.revision+1,next_evaluation=excluded.next_evaluation RETURNING revision`,
			owner, p.Zone, p.Current, p.Advance, p.AdvanceDay, p.AdvanceMinute, at.Unix()).Row().Scan(&p.Revision)
		if err != nil {
			return err
		}
		// An explicit opt-out retires any not-yet-sent copies. A later opt-in
		// cannot revive those copies in a batch that has already started.
		if (previous.Current && !p.Current) || (previous.Advance && !p.Advance) {
			if err := tx.Exec(`UPDATE reminder_dispatches SET state='suppressed' WHERE state='pending' AND occurrence IN (
SELECT id FROM reminder_occurrences WHERE incarnation=? AND ((purpose='current' AND ?) OR (purpose='advance' AND ?)))`,
				owner, previous.Current && !p.Current, previous.Advance && !p.Advance).Error; err != nil {
				return err
			}
		}
		if !p.Current && !p.Advance {
			return tx.Exec("DELETE FROM reminder_installations WHERE incarnation=?", owner).Error
		}
		return nil
	})
	return p, err
}

func reminderFamily(tx *gorm.DB, actor PersonalActor, family, platform string, at time.Time) error {
	var eligible bool
	err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM native_families f JOIN native_accesses a ON a.family_id=f.id
JOIN sessions s ON s.hash=f.anchor_hash WHERE f.id=? AND f.platform=? AND a.hash=? AND s.user_id=? AND f.expires_unix>? AND s.expires_unix>?)`,
		family, platform, actor.SessionHash, actor.UserID, at.Unix(), at.Unix()).Row().Scan(&eligible)
	if err != nil {
		return err
	}
	if !eligible {
		return ErrReminderPermission
	}
	return nil
}

func (s *ReminderSettings) Register(ctx context.Context, actor PersonalActor, family, platform, provider, token string, osAllowed bool) error {
	if !osAllowed || !s.providerEnabled(provider) {
		return ErrReminderPermission
	}
	validAPNs := platform == "ios" && provider == "apns" && apnsTokenShape.MatchString(token)
	validFCM := platform == "android" && provider == "fcm" && tokenShape.MatchString(token)
	if !validPersonalID(family) || (!validAPNs && !validFCM) {
		return ErrReminderInput
	}
	return s.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		at := s.now()
		owner, err := personalAuthority(tx, actor, at)
		if err != nil {
			return err
		}
		if err := reminderFamily(tx, actor, family, platform, at); err != nil {
			return err
		}
		prefs, err := loadReminderPreferences(tx, owner)
		if err != nil {
			return err
		}
		if !prefs.Current {
			premium, err := s.reminderPremium(ctx, tx, actor.UserID, owner, at)
			if err != nil {
				return err
			}
			if !prefs.Advance || !premium {
				return ErrReminderPermission
			}
		}
		mac := hmac.New(sha256.New, s.key)
		_, _ = mac.Write([]byte("reminder-token:" + provider + ":" + token))
		tag := mac.Sum(nil)
		ciphertext := s.vault.Seal(nil, nil, []byte(token), []byte(owner+":"+family+":"+provider))
		if err := tx.Exec("DELETE FROM reminder_installations WHERE token_tag=? AND family_id<>?", tag, family).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO reminder_installations(family_id,incarnation,platform,provider,token_tag,token_cipher,refreshed_unix)
VALUES(?,?,?,?,?,?,?) ON CONFLICT(family_id) DO UPDATE SET token_tag=excluded.token_tag,token_cipher=excluded.token_cipher,
refreshed_unix=excluded.refreshed_unix`, family, owner, platform, provider, tag, ciphertext, at.Unix()).Error
	})
}

func (s *ReminderSettings) Unregister(ctx context.Context, actor PersonalActor, family, platform string) error {
	return s.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		at := s.now()
		owner, err := personalAuthority(tx, actor, at)
		if err != nil {
			return err
		}
		if err := reminderFamily(tx, actor, family, platform, at); err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE reminder_dispatches SET state='suppressed' WHERE family_id=? AND state='pending'
AND occurrence IN (SELECT id FROM reminder_occurrences WHERE incarnation=?)`, family, owner).Error; err != nil {
			return err
		}
		return tx.Exec("DELETE FROM reminder_installations WHERE incarnation=? AND family_id=?", owner, family).Error
	})
}
