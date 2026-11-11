package store

import (
	"context"
	"database/sql"
	"errors"
	"net/mail"
	"strings"

	"github.com/LowGok/my-chef/migrations"
	"golang.org/x/text/cases"
	"gorm.io/gorm"
)

var ErrEmailAddress = errors.New("invalid email address")

// EmailIdentity keeps the delivery spelling separate from the lookup key.
func EmailIdentity(address string) (display, key string, err error) {
	display = strings.TrimSpace(address)
	if len(display) == 0 || len(display) > 254 {
		return "", "", ErrEmailAddress
	}
	parsed, err := mail.ParseAddress(display)
	if err != nil || parsed.Address != display {
		return "", "", ErrEmailAddress
	}
	return display, cases.Fold().String(display), nil
}

// BeforeCreate also covers callers that write users through the shared store.
func (u *User) BeforeCreate(_ *gorm.DB) error {
	display, key, err := EmailIdentity(u.Email)
	if err != nil {
		return err
	}
	u.Email, u.EmailKey = display, key
	return nil
}

func (s *Store) migrateEmailIdentity(ctx context.Context) error {
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
	if version != 4 || checksum != emailSchemaChecksum() || dirty {
		return errors.New("unexpected schema before email identity migration")
	}
	if err = backfillEmailIdentities(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_migrations SET version=5,checksum=?,dirty=0 WHERE version=4", emailIdentitySchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

func backfillEmailIdentities(ctx context.Context, tx *sql.Tx) error {
	type identity struct {
		id           int64
		display, key string
	}
	rows, err := tx.QueryContext(ctx, "SELECT user_id,email FROM users ORDER BY user_id")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	seen := make(map[string]struct{})
	var users []identity
	for rows.Next() {
		var u identity
		var address string
		if err := rows.Scan(&u.id, &address); err != nil {
			return err
		}
		u.display, u.key, err = EmailIdentity(address)
		if err != nil {
			return errors.New("email identity migration requires a valid address for every account")
		}
		if _, duplicate := seen[u.key]; duplicate {
			return errors.New("canonical email collision; resolve the ambiguous accounts before migrating")
		}
		seen[u.key] = struct{}{}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, migrations.EmailIdentity); err != nil {
		return err
	}
	for _, u := range users {
		if _, err := tx.ExecContext(ctx, "UPDATE users SET email=?,email_key=? WHERE user_id=?", u.display, u.key, u.id); err != nil {
			return err
		}
	}
	return nil
}
