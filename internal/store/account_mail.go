package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
)

var ErrAccountMailOwnership = errors.New("legacy account email ownership requires reconciliation")

const AccountDeletedTemplate = "account-deleted/v1"

// LinkAccountMail records the creating account, never inferring ownership from
// a recipient address that can change or later belong to another account.
func LinkAccountMail(tx *gorm.DB, user, outbox int64) error {
	if user == 0 {
		return nil
	}
	var incarnation string
	if err := tx.Raw("SELECT incarnation FROM account_incarnations WHERE user_id=?", user).Row().Scan(&incarnation); err != nil {
		return ErrAccountAccess
	}
	if err := tx.Exec("INSERT INTO account_mail_owners(outbox_id,incarnation) VALUES(?,?) ON CONFLICT(outbox_id) DO NOTHING", outbox, incarnation).Error; err != nil {
		return ErrAccountExport
	}
	var saved string
	if err := tx.Raw("SELECT incarnation FROM account_mail_owners WHERE outbox_id=?", outbox).Row().Scan(&saved); err != nil || saved != incarnation {
		return ErrAccountAccess
	}
	return nil
}

func backfillLifecycleMail(ctx context.Context, tx *sql.Tx) error {
	for _, query := range []string{
		`INSERT OR IGNORE INTO account_mail_owners SELECT r.outbox_id,a.incarnation FROM feedback_reports r JOIN account_incarnations a ON a.user_id=r.user_id`,
		`INSERT OR IGNORE INTO account_mail_owners SELECT o.id,a.incarnation FROM email_outbox o JOIN email_verification_proofs p ON o.key='verify-email/'||p.id JOIN account_incarnations a ON a.user_id=p.user_id`,
		`INSERT OR IGNORE INTO account_mail_owners SELECT o.id,a.incarnation FROM email_outbox o JOIN recovery_proofs p ON o.key='password-recovery/'||p.accepted_order OR o.key='password-reset/'||p.accepted_order JOIN account_incarnations a ON a.user_id=p.user_id`,
		`INSERT OR IGNORE INTO account_mail_owners SELECT o.id,a.incarnation FROM email_outbox o JOIN pending_email_changes p ON substr(o.key,1,length('email-change/'||p.id||'/'))='email-change/'||p.id||'/' JOIN account_incarnations a ON a.user_id=p.user_id`,
	} {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	var unknown int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM email_outbox o LEFT JOIN account_mail_owners a ON a.outbox_id=o.id WHERE a.outbox_id IS NULL AND (o.template IN ('feedback-operator/v1','password-changed/v1','email-changed/v1','verify-email/v1','email-verification/v1','password-recovery/v1','email-change-current/v1','email-change-proposed/v1') OR (o.template='signup-proof/v1' AND EXISTS(SELECT 1 FROM signup_proofs p WHERE o.key='signup/'||p.id AND p.consumed IS NOT NULL))) AND (o.recipient!='' OR o.data NOT IN ('','{}'))`).Scan(&unknown); err != nil {
		return err
	}
	if unknown != 0 {
		return ErrAccountMailOwnership
	}
	return nil
}

// SweepDeletionMail does not depend on provider configuration or worker health.
func (s *Store) SweepDeletionMail(ctx context.Context, now time.Time) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE email_outbox SET recipient='',data='{}',fingerprint='',state=CASE WHEN state IN ('queued','sending') THEN 'failed' ELSE state END,reason=CASE WHEN state IN ('queued','sending') THEN 'deletion_notice_expired' ELSE reason END,updated_at=? WHERE template=? AND (state NOT IN ('queued','sending') OR created_at<=?) AND (recipient!='' OR data!='{}')`, now.Unix(), AccountDeletedTemplate, now.Add(-23*time.Hour).Unix()).Error
	})
}
