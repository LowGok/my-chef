package analytics

import (
	"context"
	"database/sql"
	"errors"

	"github.com/LowGok/my-chef/internal/analyticsdataset"
	"gorm.io/gorm"
)

type accountControl struct {
	Revision  int64
	Changed   int64
	Objection bool
}

func (s *Service) control(ctx context.Context, inc string) (accountControl, error) {
	var c accountControl
	e := s.privacy.QueryRowContext(ctx, "SELECT revision,objection,changed FROM account_controls WHERE incarnation=?", inc).Scan(&c.Revision, &c.Objection, &c.Changed)
	if errors.Is(e, sql.ErrNoRows) {
		return c, nil
	}
	return c, e
}
func (s *Service) controlledPreference(ctx context.Context, inc string, p preferenceRow) (preferenceRow, error) {
	c, e := s.control(ctx, inc)
	if e != nil {
		return p, ErrPrivacy
	}
	if c.Revision > p.Revision || c.Objection && p.Source != "objection" {
		p.Enabled = false
		p.Subject = ""
		p.Epoch = ""
		p.Revision = max(p.Revision, c.Revision)
		if c.Objection {
			p.Source = "objection"
			p.DeletionStatus = "pending"
		} else {
			p.Source = "restore_paused"
		}
	}
	return p, nil
}
func (s *Service) prepareEnrollment(ctx context.Context, sub, inc, epoch string, revision int64) error {
	tx, e := s.privacy.BeginTx(ctx, nil)
	if e != nil {
		return ErrPrivacy
	}
	defer func() { _ = tx.Rollback() }()
	var old int64
	e = tx.QueryRowContext(ctx, "SELECT revision FROM account_controls WHERE incarnation=?", inc).Scan(&old)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return ErrPrivacy
	}
	if old >= revision {
		return ErrConflict
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO links(subject,incarnation,epoch,state,created) VALUES(?,?,?,'prepared',?) ON CONFLICT(subject) DO UPDATE SET epoch=excluded.epoch,state='prepared' WHERE links.state='active' AND links.incarnation=excluded.incarnation`, sub, inc, epoch, s.now().Unix()); e != nil {
		return ErrPrivacy
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO account_controls VALUES(?,?,0,?) ON CONFLICT(incarnation) DO UPDATE SET revision=excluded.revision,objection=0,changed=excluded.changed", inc, revision, s.now().Unix()); e != nil {
		return ErrPrivacy
	}
	if _, e = tx.ExecContext(ctx, "UPDATE privacy_meta SET generation=generation+1 WHERE singleton=1"); e != nil {
		return ErrPrivacy
	}
	if e = tx.Commit(); e != nil {
		return ErrPrivacy
	}
	return s.writeAnchor(ctx)
}
func (s *Service) authorizedLink(ctx context.Context, inc string, p preferenceRow) (bool, error) {
	var n int
	e := s.privacy.QueryRowContext(ctx, `SELECT count(*) FROM links l JOIN account_controls c ON c.incarnation=l.incarnation WHERE l.subject=? AND l.epoch=? AND l.incarnation=? AND l.state='active' AND c.revision=? AND c.objection=0`, p.Subject, p.Epoch, inc, p.Revision).Scan(&n)
	return n == 1, e
}
func (s *Service) restoreControls(ctx context.Context) error {
	rows, e := s.privacy.QueryContext(ctx, "SELECT incarnation,revision,objection FROM account_controls")
	if e != nil {
		return e
	}
	type cRow struct {
		inc       string
		revision  int64
		objection bool
	}
	var controls []cRow
	for rows.Next() {
		var c cRow
		if e = rows.Scan(&c.inc, &c.revision, &c.objection); e != nil {
			_ = rows.Close()
			return e
		}
		controls = append(controls, c)
	}
	if e = rows.Close(); e != nil {
		return e
	}
	for _, c := range controls {
		var n int64
		if e = s.app.WithContext(ctx).Raw("SELECT count(*) FROM account_incarnations WHERE incarnation=?", c.inc).Row().Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			continue
		}
		revision := c.revision + 1
		if e = s.privacyChange(ctx, "UPDATE account_controls SET revision=? WHERE incarnation=?", revision, c.inc); e != nil {
			return e
		}
		source := "restore_paused"
		deletion := "none"
		if c.objection {
			source = "objection"
			deletion = "pending"
		}
		if e = s.app.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return tx.Exec(`INSERT INTO analytics_preferences(incarnation,enabled,revision,changed_unix,source,deletion_status) VALUES(?,0,?,?,?,?)`, c.inc, revision, s.now().Unix(), source, deletion).Error
		}); e != nil {
			return e
		}
	}
	return nil
}

func (s *Service) pendingDeletion(ctx context.Context, inc string) (string, error) {
	var pending, backups int
	e := s.privacy.QueryRowContext(ctx, "SELECT coalesce(sum(cleanup='pending'),0),coalesce(sum(cleanup='backup_pending'),0) FROM links WHERE incarnation=? AND state='revoked'", inc).Scan(&pending, &backups)
	if e != nil {
		return "", e
	}
	if pending > 0 {
		return "pending", nil
	}
	if backups > 0 {
		return "backup_pending", nil
	}
	return "", nil
}

func (s *Service) reconcileObjections(ctx context.Context) error {
	rows, e := s.privacy.QueryContext(ctx, "SELECT incarnation,revision,changed FROM account_controls WHERE objection=1")
	if e != nil {
		return e
	}
	type objected struct {
		inc               string
		revision, changed int64
	}
	var controls []objected
	for rows.Next() {
		var c objected
		if e = rows.Scan(&c.inc, &c.revision, &c.changed); e != nil {
			_ = rows.Close()
			return e
		}
		controls = append(controls, c)
	}
	if e = rows.Close(); e != nil {
		return e
	}
	for _, c := range controls {
		var exists int64
		if e = s.app.WithContext(ctx).Raw("SELECT count(*) FROM account_incarnations WHERE incarnation=?", c.inc).Row().Scan(&exists); e != nil {
			return e
		}
		if exists == 0 {
			continue
		}
		status, e := s.pendingDeletion(ctx, c.inc)
		if e != nil {
			return e
		}
		if status == "" {
			status = "complete"
		}
		if e = s.app.WithContext(ctx).Exec(`INSERT INTO analytics_preferences(incarnation,enabled,revision,changed_unix,source,deletion_status) VALUES(?,0,?,?,'objection',?) ON CONFLICT(incarnation) DO UPDATE SET enabled=0,revision=max(revision,excluded.revision),subject='',epoch='',source='objection',deletion_status=excluded.deletion_status`, c.inc, c.revision, c.changed, status).Error; e != nil {
			return e
		}
	}
	return nil
}

func (s *Service) retireCompletedControls(ctx context.Context) error {
	rows, e := s.privacy.QueryContext(ctx, "SELECT path FROM copies WHERE kind='extract' AND state='ready'")
	if e != nil {
		return e
	}
	var paths []string
	for rows.Next() {
		var path string
		if e = rows.Scan(&path); e != nil {
			_ = rows.Close()
			return e
		}
		paths = append(paths, path)
	}
	if e = rows.Close(); e != nil {
		return e
	}
	referenced := map[string]bool{}
	for _, path := range paths {
		d, e := analyticsdataset.OpenProduction(ctx, path, s.binding, true)
		if e != nil {
			return e
		}
		ids, e := d.ProductionSubjects(ctx, true)
		e = errors.Join(e, d.Close())
		if e != nil {
			return e
		}
		for _, id := range ids {
			referenced[id] = true
		}
	}
	done, e := s.privacy.QueryContext(ctx, "SELECT subject FROM links WHERE state='revoked' AND cleanup='complete'")
	if e != nil {
		return e
	}
	var ids []string
	for done.Next() {
		var id string
		if e = done.Scan(&id); e != nil {
			_ = done.Close()
			return e
		}
		ids = append(ids, id)
	}
	if e = done.Close(); e != nil {
		return e
	}
	for _, id := range ids {
		if referenced[id] {
			_, e = s.privacy.ExecContext(ctx, "UPDATE links SET incarnation='',epoch='',created=0,revoked=NULL WHERE subject=?", id)
		} else {
			_, e = s.privacy.ExecContext(ctx, "DELETE FROM links WHERE subject=?", id)
		}
		if e != nil {
			return e
		}
	}
	// Minimal current objections remain for a live account. Finished deleted
	// accounts no longer need their account-incarnation association.
	controls, e := s.privacy.QueryContext(ctx, "SELECT incarnation FROM account_controls WHERE objection=1")
	if e != nil {
		return e
	}
	var incarnations []string
	for controls.Next() {
		var inc string
		if e = controls.Scan(&inc); e != nil {
			_ = controls.Close()
			return e
		}
		incarnations = append(incarnations, inc)
	}
	if e = controls.Close(); e != nil {
		return e
	}
	for _, inc := range incarnations {
		var account int64
		if e = s.app.WithContext(ctx).Raw("SELECT count(*) FROM account_incarnations WHERE incarnation=?", inc).Row().Scan(&account); e != nil {
			return e
		}
		if account == 0 {
			if _, e = s.privacy.ExecContext(ctx, "DELETE FROM account_controls WHERE incarnation=? AND NOT EXISTS(SELECT 1 FROM links WHERE incarnation=?)", inc, inc); e != nil {
				return e
			}
		}
	}
	return nil
}
