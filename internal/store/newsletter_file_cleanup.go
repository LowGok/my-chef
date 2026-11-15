package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
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
