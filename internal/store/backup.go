package store

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/LowGok/my-chef/internal/analytics"
	"github.com/ncruces/go-sqlite3/driver"
	"gorm.io/gorm"
)

// Validate checks the application schema and SQLite's structural and FK integrity.
func (s *Store) Validate(ctx context.Context) error {
	if err := s.CheckSchema(ctx); err != nil {
		return err
	}
	return s.validateIntegrity(ctx)
}

func (s *Store) validateIntegrity(ctx context.Context) error {
	var result string
	if err := s.SQL.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errors.New("database integrity check failed")
	}
	rows, err := s.SQL.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		return errors.New("database foreign key check failed")
	}
	return rows.Err()
}

// Backup uses SQLite's online backup API, including committed WAL contents.
// An existing destination is never replaced. The application can keep serving.
func (s *Store) Backup(ctx context.Context, destination string) error {
	if err := s.CheckSchema(ctx); err != nil {
		return err
	}
	var stage string
	if err := s.SQL.QueryRowContext(ctx, "SELECT stage FROM lifecycle_binding WHERE singleton=1").Scan(&stage); err != nil || stage != "ready" {
		return ErrLifecycleJournal
	}
	if s.analyticsRequired(ctx) {
		if s.Analytics == nil {
			return analytics.ErrPrivacy
		}
		return s.Analytics.WithAppStaging(ctx, func(stage string) error {
			if err := s.writeSnapshot(ctx, stage, false); err != nil {
				return err
			}
			return publishDatabase(ctx, destination, func(temp string) error {
				in, e := os.Open(stage)
				if e != nil {
					return e
				}
				defer func() { _ = in.Close() }()
				out, e := os.OpenFile(temp, os.O_WRONLY|os.O_TRUNC, 0600)
				if e != nil {
					return e
				}
				_, e = io.Copy(out, in)
				if e == nil {
					e = out.Sync()
				}
				return errors.Join(e, out.Close())
			})
		})
	}
	return s.publishSnapshot(ctx, destination, false)
}

func (s *Store) publishSnapshot(ctx context.Context, destination string, upgrade bool, prepare ...func(*Store) error) error {
	return publishDatabase(ctx, destination, func(temp string) error { return s.writeSnapshot(ctx, temp, upgrade, prepare...) })
}
func (s *Store) writeSnapshot(ctx context.Context, temp string, upgrade bool, prepare ...func(*Store) error) error {
	conn, err := s.SQL.Conn(ctx)
	if err != nil {
		return err
	}
	err = conn.Raw(func(raw any) error {
		sqlite := raw.(driver.Conn).Raw()
		old := sqlite.SetInterrupt(ctx)
		defer sqlite.SetInterrupt(old)
		return sqlite.Backup("main", temp)
	})
	_ = conn.Close()
	if err != nil {
		return err
	}
	check, err := Open(temp, false)
	if err != nil {
		return err
	}
	defer func() { _ = check.Close() }()
	if upgrade {
		if err = check.Migrate(ctx); err != nil {
			return err
		}
	}
	if err = check.stripAnalytics(ctx); err != nil {
		return err
	}
	for _, apply := range prepare {
		if err = apply(check); err != nil {
			return err
		}
	}
	if err = check.Validate(ctx); err != nil {
		return err
	}
	var busy, log, checkpointed int
	if err = check.SQL.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("backup checkpoint is busy")
	}
	var mode string
	if err = check.SQL.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode); err != nil {
		return err
	}
	if _, err = check.SQL.ExecContext(ctx, "VACUUM"); err != nil {
		return err
	}
	if mode != "delete" {
		return errors.New("backup requires sidecar files")
	}
	return check.Close()
}

// Restore publishes a verified backup onto an absent database on a fresh volume.
// An older supported snapshot is upgraded on a temporary copy before publication.
// Hold the application lock so a concurrently starting server cannot use it.
type AccountRestoreOptions struct {
	Analytics   *analytics.OpenOptions
	JournalPath string
	TraceRoot   string
	Cleanup     func(context.Context, *gorm.DB, User) error
}

func Restore(ctx context.Context, backup, destination string, options ...AccountRestoreOptions) error {
	unlock, err := LockApplication(destination)
	if err != nil {
		return err
	}
	defer unlock()
	source, err := OpenReadOnly(backup)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	if err = source.checkSchema(ctx, true); err != nil {
		return err
	}
	if err = source.validateIntegrity(ctx); err != nil {
		return err
	}
	opts := AccountRestoreOptions{JournalPath: destination + ".lifecycle"}
	if len(options) > 0 {
		opts = options[0]
	}
	var hasAnalytics int
	_ = source.SQL.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name='analytics_installation'").Scan(&hasAnalytics)
	if hasAnalytics > 0 && source.analyticsRequired(ctx) && opts.Analytics == nil {
		return analytics.ErrPrivacy
	}
	if opts.JournalPath == "" {
		return ErrLifecycleJournal
	}
	// Older snapshots cannot receive new incarnations and bypass old markers.
	var binding string
	if err := source.SQL.QueryRowContext(ctx, "SELECT binding FROM lifecycle_binding WHERE singleton=1").Scan(&binding); err != nil {
		return ErrLifecycleJournal
	}
	for _, table := range []string{"analytics_outbox", "analytics_preferences", "analytics_receipts", "analytics_generation", "analytics_signup", "analytics_gaps"} {
		var exists int
		if err = source.SQL.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?", table).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			var rows int
			if err = source.SQL.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&rows); err != nil || rows != 0 {
				return analytics.ErrPrivacy
			}
		}
	}
	// Only this read-only restore source may have an earlier known app schema.
	// The journal itself must already be current and matched; the temporary
	// application copy is explicitly upgraded before deletion reconciliation.
	journal, err := source.lifecycleJournal(ctx, opts.JournalPath, false, false, true)
	if err != nil {
		return err
	}
	if err = journal.ReconcileIntents(ctx); err != nil {
		_ = journal.Close()
		return err
	}
	_ = journal.Close()
	return source.publishSnapshot(ctx, destination, true, func(copy *Store) error {
		paired, err := copy.OpenLifecycleJournal(ctx, opts.JournalPath)
		if err != nil {
			return err
		}
		defer func() { _ = paired.Close() }()
		deletion := AccountDeletionOptions{Capture: func(tx *gorm.DB, u User, inc string) error {
			return paired.CaptureAccountTraces(ctx, tx, u.ID, inc, opts.TraceRoot)
		}}
		if opts.Cleanup != nil {
			deletion.Cleanup = func(tx *gorm.DB, u User) error { return opts.Cleanup(ctx, tx, u) }
		}
		if err := paired.ReconcileAccountDeletions(ctx, deletion); err != nil {
			return err
		}
		if opts.Analytics != nil {
			settings := *opts.Analytics
			settings.Create = false
			service, e := analytics.Open(ctx, copy.DB, settings)
			if e != nil {
				return e
			}
			defer func() { _ = service.Close() }()
			if e = service.ReconcileAppRestore(ctx); e != nil {
				return e
			}
		}
		// Old snapshots must never resurrect consumed refresh credentials.
		// The trigger also removes native access actors and their anchors.
		return copy.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := resetRestoredNewsletter(ctx, tx, time.Now().UTC()); err != nil {
				return err
			}
			if err := tx.Exec("DELETE FROM native_families").Error; err != nil {
				return err
			}
			// Fixture authorization is ephemeral across a restore, even when
			// the account and its configured test lease are still valid.
			return retireFixtureEntitlements(tx)
		})
	})
}

func publishDatabase(ctx context.Context, destination string, write func(string) error) error {
	target, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(target); err == nil {
		return errors.New("destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".mychef-backup-*.db")
	if err != nil {
		return err
	}
	temp := file.Name()
	if err = file.Close(); err != nil {
		return err
	}
	defer func() {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(temp + suffix)
		}
	}()
	if err = write(temp); err != nil {
		return err
	}
	file, err = os.Open(temp)
	if err != nil {
		return err
	}
	err = file.Sync()
	_ = file.Close()
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Link(temp, target); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
