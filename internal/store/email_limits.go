package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/LowGok/my-chef/migrations"
	"gorm.io/gorm"
)

const maxEmailLimitWindow = 24 * time.Hour

var ErrEmailLimitUnavailable = errors.New("email rate limit unavailable")

type EmailLimit struct {
	Bucket  string
	Maximum int
	Window  time.Duration
}

type EmailLimitResult struct {
	Reservation string
	RetryAfter  int
}

// EmailRateBucket derives a stable, purpose-bound key without storing an
// address or token in plaintext.
func EmailRateBucket(key []byte, purpose, value string) (string, error) {
	if len(key) != 32 || purpose == "" || len(purpose) > 64 || strings.ContainsRune(purpose, 0) || len(value) > 1024 {
		return "", ErrEmailLimitUnavailable
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte("mychef/email-rate/v1/" + purpose + "\x00"))
	h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Store) migrateEmailLimits(ctx context.Context) error {
	tx, err := s.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var checksum string
	var dirty bool
	if err = tx.QueryRowContext(ctx, "SELECT version,checksum,dirty FROM schema_migrations").Scan(&version, &checksum, &dirty); err != nil {
		return err
	}
	if version != 5 || checksum != emailIdentitySchemaChecksum() || dirty {
		return errors.New("unexpected schema before email limits migration")
	}
	if _, err = tx.ExecContext(ctx, migrations.EmailLimits); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_migrations SET version=6,checksum=?,dirty=0 WHERE version=5", emailLimitsSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

func validEmailLimits(limits []EmailLimit) bool {
	if len(limits) == 0 || len(limits) > 8 {
		return false
	}
	seen := make(map[string]bool, len(limits))
	for _, limit := range limits {
		if limit.Bucket == "" || len(limit.Bucket) > 128 || seen[limit.Bucket] ||
			limit.Maximum < 1 || limit.Maximum > 1000 || limit.Window <= 0 || limit.Window > maxEmailLimitWindow {
			return false
		}
		seen[limit.Bucket] = true
	}
	return true
}

// EmailLimitsRetryAfter reads an existing transaction's limits without
// charging them. Only check buckets the caller is authorized to inspect.
func (s *Store) EmailLimitsRetryAfter(ctx context.Context, tx *gorm.DB, now time.Time, limits ...EmailLimit) (int, error) {
	if tx == nil || !validEmailLimits(limits) {
		return 0, ErrEmailLimitUnavailable
	}
	wait, err := emailLimitsRetryAfter(tx.WithContext(ctx), now, limits)
	if err != nil {
		return 0, ErrEmailLimitUnavailable
	}
	return wait, nil
}

func emailLimitsRetryAfter(db *gorm.DB, now time.Time, limits []EmailLimit) (int, error) {
	var retry int
	for _, limit := range limits {
		var blockingStamp int64
		err := db.Raw(`SELECT stamp FROM email_rate_events WHERE bucket=? AND stamp>?
ORDER BY stamp DESC LIMIT 1 OFFSET ?`, limit.Bucket, now.Add(-limit.Window).UnixNano(), limit.Maximum-1).
			Row().Scan(&blockingStamp)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, err
		}
		wait := time.Duration(blockingStamp-now.UnixNano()) + limit.Window
		retry = max(retry, int((wait+time.Second-1)/time.Second))
	}
	return retry, nil
}

// ReserveEmailLimits charges all supplied rolling buckets together. No
// reservation is created when any bucket is already full.
func (s *Store) ReserveEmailLimits(ctx context.Context, tx *gorm.DB, now time.Time, limits ...EmailLimit) (EmailLimitResult, error) {
	var result EmailLimitResult
	if !validEmailLimits(limits) {
		return result, ErrEmailLimitUnavailable
	}
	charge := func(db *gorm.DB) error {
		if err := db.Exec("DELETE FROM email_rate_events WHERE stamp <= ?", now.Add(-maxEmailLimitWindow).UnixNano()).Error; err != nil {
			return err
		}
		var err error
		result.RetryAfter, err = emailLimitsRetryAfter(db, now, limits)
		if err != nil {
			return err
		}
		if result.RetryAfter != 0 {
			return nil
		}
		result.Reservation, err = addEmailReservation(db, now, limits)
		return err
	}
	var err error
	if tx == nil {
		err = s.DB.WithContext(ctx).Transaction(charge)
	} else {
		err = charge(tx.WithContext(ctx))
	}
	if err != nil {
		return EmailLimitResult{}, ErrEmailLimitUnavailable
	}
	return result, nil
}

func addEmailReservation(db *gorm.DB, now time.Time, limits []EmailLimit) (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	reservation := hex.EncodeToString(id[:])
	for _, limit := range limits {
		if err := db.Exec("INSERT INTO email_rate_events(bucket,stamp,reservation) VALUES(?,?,?)",
			limit.Bucket, now.UnixNano(), reservation).Error; err != nil {
			return "", err
		}
	}
	return reservation, nil
}

// ReserveRecoveryCompletion always charges an admitted source, even when
// a full proof bucket prevents work. Both decisions use one transaction.
func (s *Store) ReserveRecoveryCompletion(ctx context.Context, now time.Time, source, proof string) (int, error) {
	const window = 15 * time.Minute
	limits := []EmailLimit{{Bucket: source, Maximum: 50, Window: window}}
	if proof != "" {
		limits = append(limits, EmailLimit{Bucket: proof, Maximum: 10, Window: window})
	}
	if !validEmailLimits(limits) {
		return 0, ErrEmailLimitUnavailable
	}
	var wait int
	err := s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if err := db.Exec("DELETE FROM email_rate_events WHERE stamp <= ?", now.Add(-maxEmailLimitWindow).UnixNano()).Error; err != nil {
			return err
		}
		sourceWait, err := emailLimitsRetryAfter(db, now, limits[:1])
		if err != nil {
			return err
		}
		proofWait, err := emailLimitsRetryAfter(db, now, limits[1:])
		if err != nil {
			return err
		}
		wait = max(sourceWait, proofWait)
		if sourceWait != 0 {
			return nil
		}
		if proofWait != 0 {
			limits = limits[:1]
		}
		_, err = addEmailReservation(db, now, limits)
		return err
	})
	if err != nil {
		return 0, ErrEmailLimitUnavailable
	}
	return wait, nil
}

// ReleaseEmailLimit uncharges one bucket of a reserved attempt, leaving the
// source attempt available for independent enforcement.
func (s *Store) ReleaseEmailLimit(ctx context.Context, tx *gorm.DB, reservation, bucket string) error {
	return s.deleteEmailLimits(ctx, tx, "DELETE FROM email_rate_events WHERE bucket=? AND reservation=?", bucket, reservation)
}

func (s *Store) ClearEmailLimit(ctx context.Context, tx *gorm.DB, bucket string) error {
	return s.deleteEmailLimits(ctx, tx, "DELETE FROM email_rate_events WHERE bucket=?", bucket)
}

func (s *Store) deleteEmailLimits(ctx context.Context, tx *gorm.DB, query string, values ...any) error {
	if tx == nil {
		tx = s.DB
	}
	if err := tx.WithContext(ctx).Exec(query, values...).Error; err != nil {
		return ErrEmailLimitUnavailable
	}
	return nil
}
