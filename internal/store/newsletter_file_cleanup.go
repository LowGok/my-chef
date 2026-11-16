package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// NewsletterFilesReady is checked before capture, outside an application SQL
// transaction. Proof withdrawal uses its own identity authority and stays usable
// while a healthy store still has pending physical maintenance.
func (j *LifecycleJournal) NewsletterFilesReady(ctx context.Context) error {
	if err := j.CheckReady(ctx); err != nil {
		return err
	}
	if _, bound, err := j.newsletterIdentityBinding(ctx); err != nil || !bound {
		return ErrNewsletterIdentityUnavailable
	}
	var pending, legacy bool
	if err := j.app.SQL.QueryRowContext(ctx, "SELECT physical_cleanup_pending,legacy_vacuum_pending FROM newsletter_identity_namespace WHERE singleton=1").Scan(&pending, &legacy); err != nil || pending || legacy {
		return ErrNewsletterIdentityUnavailable
	}
	for _, db := range []*Store{j.app, j.store} {
		var enabled int
		if err := db.SQL.QueryRowContext(ctx, "PRAGMA secure_delete").Scan(&enabled); err != nil || enabled != 1 {
			return ErrNewsletterIdentityUnavailable
		}
	}
	return nil
}

// CleanupNewsletterFiles owns the account fence once, reserves each namespace's
// single SQL connection and leaves the durable marker set on every failed step.
// It never touches historical snapshots or substitutes another journal/key.
func (j *LifecycleJournal) CleanupNewsletterFiles(ctx context.Context) error {
	j.app.AccountWork.Lock()
	defer j.app.AccountWork.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := j.CheckReady(ctx); err != nil {
		return err
	}
	if _, bound, err := j.newsletterIdentityBinding(ctx); err != nil || !bound {
		return ErrNewsletterIdentityUnavailable
	}
	var pending, legacy bool
	if err := j.app.SQL.QueryRowContext(ctx, "SELECT physical_cleanup_pending,legacy_vacuum_pending FROM newsletter_identity_namespace WHERE singleton=1").Scan(&pending, &legacy); err != nil {
		return err
	}
	if !pending && !legacy {
		return j.NewsletterFilesReady(ctx)
	}
	if _, err := j.app.SQL.ExecContext(ctx, "UPDATE newsletter_identity_namespace SET physical_cleanup_pending=1 WHERE singleton=1"); err != nil {
		return err
	}
	for _, db := range []*Store{j.app, j.store} {
		if err := cleanupNewsletterSQLite(ctx, db.SQL, legacy); err != nil {
			return err
		}
	}
	result, err := j.app.SQL.ExecContext(ctx, "UPDATE newsletter_identity_namespace SET physical_cleanup_pending=0,legacy_vacuum_pending=0 WHERE singleton=1")
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrNewsletterIdentityUnavailable
	}
	return nil
}

func cleanupNewsletterSQLite(ctx context.Context, db *sql.DB, legacy bool) error {
	connection, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	remaining := 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		remaining = min(remaining, time.Until(deadline))
	}
	if remaining <= 0 {
		return context.DeadlineExceeded
	}
	busy := max(1, remaining.Milliseconds())
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout(%d)", busy)); err != nil {
		return err
	}
	defer func() {
		reset, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_, _ = connection.ExecContext(reset, "PRAGMA busy_timeout(5000)")
	}()
	if _, err := connection.ExecContext(ctx, "PRAGMA secure_delete=ON"); err != nil {
		return err
	}
	var enabled int
	if err := connection.QueryRowContext(ctx, "PRAGMA secure_delete").Scan(&enabled); err != nil || enabled != 1 {
		return ErrNewsletterIdentityUnavailable
	}
	if err := checkpointNewsletterSQLite(ctx, connection); err != nil {
		return err
	}
	if legacy {
		if _, err := connection.ExecContext(ctx, "VACUUM"); err != nil {
			return err
		}
	}
	var integrity string
	if err := connection.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return ErrNewsletterIdentityUnavailable
	}
	rows, err := connection.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	invalid := rows.Next()
	scanErr := rows.Err()
	if err := rows.Close(); err != nil {
		return err
	}
	if invalid || scanErr != nil {
		return ErrNewsletterIdentityUnavailable
	}
	return checkpointNewsletterSQLite(ctx, connection)
}

func checkpointNewsletterSQLite(ctx context.Context, connection *sql.Conn) error {
	var busy, frames, checkpointed int
	if err := connection.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 || frames != 0 || checkpointed != 0 {
		return errors.New("newsletter checkpoint incomplete")
	}
	return nil
}

// SweepExpiredAccountProofSlice runs under the caller's account fence and SQL
// transaction. Maintenance and unpublished restore copies use the same expiry
// policy without constructing account workers or reviving old dispatch work.
func SweepExpiredAccountProofSlice(ctx context.Context, tx *gorm.DB, now time.Time) (bool, error) {
	if tx == nil || tx.Statement == nil {
		return false, ErrNewsletterIdentityUnavailable
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return false, ErrNewsletterIdentityUnavailable
	}
	tx = tx.WithContext(ctx)
	var before, after int64
	if err := tx.Raw("SELECT total_changes()").Row().Scan(&before); err != nil {
		return false, err
	}
	const batch = 100
	for _, work := range []struct {
		table, identity, predicate, mailKeys string
		args                                 []any
		limit                                int
	}{
		{"signup_requests", "id", "expires<=?", "", []any{now.Unix()}, batch},
		{"signup_proofs", "id", "expires<=? OR consumed<=?", "'signup/' || id", []any{now.Unix(), now.Add(-24 * time.Hour).Unix()}, batch},
		{"email_verification_proofs", "id", "expires<=? OR consumed<=?", "'verify-email/' || id", []any{now.Unix(), now.Add(-24 * time.Hour).Unix()}, batch},
		{"pending_email_changes", "id", "expires<=?", "'email-change/' || id || '/current'", []any{now.Unix()}, batch},
		{"recovery_jobs", "accepted_order", "expires_ns<=?", "", []any{now.UnixNano()}, 16},
		{"recovery_proofs", "hash", "expires_ns<=?", "'password-recovery/' || accepted_order", []any{now.UnixNano()}, 16},
	} {
		selection := " FROM " + work.table + " WHERE " + work.predicate + " ORDER BY " + work.identity + " LIMIT ?"
		args := append(append([]any{}, work.args...), work.limit)
		if work.mailKeys != "" {
			mailSelection := "SELECT id FROM email_outbox WHERE key IN (SELECT " + work.mailKeys + selection + ")"
			if err := deleteExpiredAccountMail(ctx, tx, mailSelection, args...); err != nil {
				return false, err
			}
			if work.table == "pending_email_changes" {
				mailSelection = "SELECT id FROM email_outbox WHERE key IN (SELECT 'email-change/' || id || '/proposed'" + selection + ")"
				if err := deleteExpiredAccountMail(ctx, tx, mailSelection, args...); err != nil {
					return false, err
				}
			}
		}
		if err := tx.Exec("DELETE FROM "+work.table+" WHERE "+work.identity+" IN (SELECT "+work.identity+selection+")", args...).Error; err != nil {
			return false, err
		}
	}
	mailSelection := `SELECT id FROM email_outbox WHERE template IN ? AND (created_at<=? OR (state='failed' AND reason IN ('proof_invalidated','proof_expired')) OR (json_valid(data) AND ((json_extract(data,'$.expires_ns') IS NOT NULL AND json_extract(data,'$.expires_ns')<=?) OR (json_extract(data,'$.expires_ns') IS NULL AND json_extract(data,'$.expires')<=?)))) ORDER BY id LIMIT ?`
	if err := deleteExpiredAccountMail(ctx, tx, mailSelection, accountProofMailTemplates, now.Add(-23*time.Hour).Unix(), now.UnixNano(), now.Unix(), batch); err != nil {
		return false, err
	}
	if err := tx.Raw("SELECT total_changes()").Row().Scan(&after); err != nil {
		return false, err
	}
	changed := after > before
	if changed {
		if err := tx.Exec("UPDATE newsletter_identity_namespace SET physical_cleanup_pending=1 WHERE singleton=1").Error; err != nil {
			return false, err
		}
	}
	return changed, nil
}

// These versioned templates are the existing purpose-owned proof deliveries.
// Ordinary messages and unrelated account state never enter this expiry sweep.
var accountProofMailTemplates = []string{
	"signup-proof/v1", "email-verification/v1", "email-change-current/v1", "email-change-proposed/v1", "password-recovery/v1",
	"newsletter-confirmation-en/v1", "newsletter-confirmation-fr/v1", "newsletter-confirmation-nl/v1",
	"newsletter-withdrawal-en/v1", "newsletter-withdrawal-fr/v1", "newsletter-withdrawal-nl/v1",
}

func deleteExpiredAccountMail(ctx context.Context, tx *gorm.DB, selection string, args ...any) error {
	var inconsistent int64
	checkArgs := append(append([]any{}, args...), accountProofMailTemplates)
	if err := tx.WithContext(ctx).Raw("SELECT count(*) FROM email_outbox WHERE id IN ("+selection+") AND template NOT IN ?", checkArgs...).Scan(&inconsistent).Error; err != nil {
		return err
	}
	if inconsistent != 0 {
		return ErrNewsletterIdentityUnavailable
	}
	if err := tx.Exec("DELETE FROM email_events WHERE provider_id IN (SELECT provider_id FROM email_outbox WHERE id IN ("+selection+"))", args...).Error; err != nil {
		return err
	}
	return tx.Exec("DELETE FROM email_outbox WHERE id IN ("+selection+")", args...).Error
}
