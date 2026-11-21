package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/LowGok/my-chef/migrations"
	"gorm.io/gorm"
)

func newsletterSchemaChecksum() string {
	sum := sha256.Sum256([]byte(analyticsGapsChecksum() + migrations.Newsletter))
	return hex.EncodeToString(sum[:])
}

// InvalidateNewsletterConsent joins an owned email-change transaction.
func InvalidateNewsletterConsent(ctx context.Context, tx *gorm.DB, userID int64, now time.Time) error {
	var order int64
	if err := tx.WithContext(ctx).Raw("UPDATE newsletter_sequence SET value=value+1 WHERE singleton=1 RETURNING value").Row().Scan(&order); err != nil {
		return err
	}
	if err := tx.Exec(`UPDATE newsletter_subscriptions SET status='unsubscribed',revision=revision+1,proved_order=?,withdrawn_unix=? WHERE user_id=?`, order, now.Unix(), userID).Error; err != nil {
		return err
	}
	if err := tx.Exec(`UPDATE newsletter_proofs SET consumed_unix=COALESCE(consumed_unix,?),proposal='' WHERE user_id=?`, now.Unix(), userID).Error; err != nil {
		return err
	}
	return tx.Exec(`UPDATE email_outbox SET data=json_remove(data,'$.links'),state=CASE WHEN state IN ('queued','sending') THEN 'failed' ELSE state END,reason=CASE WHEN state IN ('queued','sending') THEN 'proof_invalidated' ELSE reason END,updated_at=?
WHERE key LIKE 'newsletter/%' AND id IN(SELECT m.outbox_id FROM account_mail_owners m JOIN account_incarnations a ON a.incarnation=m.incarnation WHERE a.user_id=?)`, now.Unix(), userID).Error
}

// RecordNewsletterErasure prevents older anonymous jobs from recreating a user.
func RecordNewsletterErasure(ctx context.Context, tx *gorm.DB, rateKey []byte, emailKey string) error {
	tag, err := EmailRateBucket(rateKey, "newsletter/erasure", emailKey)
	if err != nil {
		return err
	}
	var order int64
	if err := tx.WithContext(ctx).Raw("UPDATE newsletter_sequence SET value=value+1 WHERE singleton=1 RETURNING value").Row().Scan(&order); err != nil {
		return err
	}
	return tx.Exec(`INSERT INTO newsletter_erasures(address_tag,through_order) VALUES(?,?) ON CONFLICT(address_tag) DO UPDATE SET through_order=MAX(through_order,excluded.through_order)`, tag, order).Error
}

// resetRestoredNewsletter requires a fresh request and confirmation after restore.
// Existing timestamps remain historical evidence, not a new consent decision.
func resetRestoredNewsletter(ctx context.Context, tx *gorm.DB, now time.Time) error {
	var order int64
	if err := tx.WithContext(ctx).Raw(`UPDATE newsletter_sequence SET value=MAX(value,(SELECT COALESCE(MAX(proved_order),0) FROM newsletter_subscriptions))+1 WHERE singleton=1 RETURNING value`).Row().Scan(&order); err != nil {
		return err
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE newsletter_subscriptions SET status='pending',revision=revision+1,proved_order=?`, []any{order}},
		{`UPDATE newsletter_proofs SET consumed_unix=COALESCE(consumed_unix,?),proposal=''`, []any{now.Unix()}},
		{`DELETE FROM newsletter_requests`, nil},
		{`UPDATE email_outbox SET data=json_remove(data,'$.links'),state=CASE WHEN state IN ('queued','sending') THEN 'failed' ELSE state END,reason=CASE WHEN state IN ('queued','sending') THEN 'proof_invalidated' ELSE reason END,updated_at=? WHERE key LIKE 'newsletter/%'`, []any{now.Unix()}},
	} {
		if err := tx.Exec(statement.query, statement.args...).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateNewsletter(ctx context.Context) error {
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
	if version != 23 || checksum != analyticsGapsChecksum() || dirty {
		return errors.New("unexpected schema before newsletter migration")
	}
	if _, err := tx.ExecContext(ctx, migrations.Newsletter); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_migrations SET version=24,checksum=?,dirty=0 WHERE version=23", newsletterSchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}
