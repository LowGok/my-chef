package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/LowGok/my-chef/migrations"
	"golang.org/x/text/cases"
)

const MaxUsernameBytes = 80

// UsernameKey folds a supported stored spelling without trimming, normalizing,
// or changing the username. Unsupported legacy rows remain email-only.
func UsernameKey(name string) []byte {
	if len(name) == 0 || len(name) > MaxUsernameBytes || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return nil
	}
	return []byte(cases.Fold().String(name))
}

func loginUsernameSchemaChecksum() string {
	sum := sha256.Sum256([]byte(newsletterIdentitySchemaChecksum() + migrations.LoginUsernameKeys))
	return hex.EncodeToString(sum[:])
}

func (s *Store) migrateLoginUsernameKeys(ctx context.Context) error {
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
	if version != 27 || checksum != newsletterIdentitySchemaChecksum() || dirty {
		return errors.New("unexpected schema before login username migration")
	}
	if _, err := tx.ExecContext(ctx, migrations.LoginUsernameKeys); err != nil {
		return err
	}
	if err := backfillLoginUsernames(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_migrations SET version=28,checksum=?,dirty=0 WHERE version=27", loginUsernameSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

func backfillLoginUsernames(ctx context.Context, tx *sql.Tx) error {
	type row struct {
		id       int64
		username sql.NullString
	}
	const first = "SELECT user_id,username FROM users WHERE isNewsletterOnly=0 ORDER BY user_id LIMIT 256"
	const next = "SELECT user_id,username FROM users WHERE isNewsletterOnly=0 AND user_id>? ORDER BY user_id LIMIT 256"
	query, args := first, []any(nil)
	for {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		var batch []row
		for rows.Next() {
			var u row
			if err := rows.Scan(&u.id, &u.username); err != nil {
				_ = rows.Close()
				return err
			}
			batch = append(batch, u)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, u := range batch {
			if key := UsernameKey(u.username.String); u.username.Valid && key != nil {
				if _, err := tx.ExecContext(ctx, "UPDATE users SET login_username_key=? WHERE user_id=?", key, u.id); err != nil {
					return err
				}
			}
		}
		query, args = next, []any{batch[len(batch)-1].id}
	}
}
