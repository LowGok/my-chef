package analytics

import (
	"context"
	"errors"
	"os"

	"github.com/LowGok/my-chef/internal/analyticsdataset"
)

func (s *Service) Maintain(ctx context.Context) error {
	if e := s.mu.LockContext(ctx); e != nil {
		return e
	}
	defer s.mu.Unlock()
	if s.closed {
		return ErrPrivacy
	}
	return s.maintainLocked(ctx)
}
func (s *Service) maintainLocked(ctx context.Context) error {
	if e := s.checkInventory(ctx); e != nil {
		return e
	}
	if e := s.checkAnchor(ctx); e != nil {
		return e
	}
	if e := s.reconcilePrepared(ctx); e != nil {
		return e
	}
	a, e := s.readAnchor(ctx)
	if e != nil {
		return e
	}
	now := s.now().Unix()
	rows, e := s.privacy.QueryContext(ctx, "SELECT subject FROM links WHERE state='revoked'")
	if e != nil {
		return e
	}
	var revoked []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			_ = rows.Close()
			return e
		}
		revoked = append(revoked, id)
	}
	if e = rows.Close(); e != nil {
		return e
	}
	tx, e := s.journal.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range revoked {
		if _, e = tx.ExecContext(ctx, "DELETE FROM source_checkpoints WHERE epoch IN(SELECT id FROM processing_periods WHERE subject=?)", id); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM delivery_receipts WHERE id IN(SELECT id FROM facts WHERE subject=?)", id); e != nil {
			return e
		}
		for _, table := range []string{"facts", "processing_periods", "contributions"} {
			if _, e = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE subject=?", id); e != nil {
				return e
			}
		}
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM facts WHERE occurred<? OR occurred>?", now-90*86400, now); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM delivery_receipts WHERE occurred<?", now-90*86400); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM contributions WHERE month<?", monthStart(now).AddDate(0, -12, 0).Format("2006-01")); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM processing_periods WHERE end IS NOT NULL AND end<? AND NOT EXISTS(SELECT 1 FROM contributions c WHERE c.subject=processing_periods.subject AND c.month>=strftime('%Y-%m',processing_periods.start,'unixepoch') AND c.month<=strftime('%Y-%m',processing_periods.end-1,'unixepoch'))", monthStart(now).AddDate(0, -12, 0).Unix()); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM source_checkpoints WHERE NOT EXISTS(SELECT 1 FROM processing_periods p WHERE p.id=source_checkpoints.epoch)"); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM delivery_batches WHERE created<?", now-90*86400); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM coverage WHERE end<?", now-90*86400); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE journal_meta SET maintenance=?,generation=? WHERE singleton=1", now, a.Generation); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	// Persist outbox retention and durable objections even after an app restore.
	expired := s.app.WithContext(ctx).Exec("DELETE FROM analytics_outbox WHERE occurred_unix<? OR occurred_unix>?", now-90*86400, now)
	if expired.Error != nil {
		return expired.Error
	}
	if expired.RowsAffected > 0 {
		s.appDirty.Store(true)
	}
	for _, id := range revoked {
		if e = s.app.WithContext(ctx).Exec("DELETE FROM analytics_outbox WHERE subject=?", id).Error; e != nil {
			return e
		}
	}
	type copyRow struct {
		path, kind, state   string
		deadline, reporting int64
	}
	copies, e := s.privacy.QueryContext(ctx, "SELECT path,kind,state,deadline,reporting FROM copies WHERE state!='deleted' AND kind!='journal'")
	if e != nil {
		return e
	}
	var list []copyRow
	for copies.Next() {
		var c copyRow
		if e = copies.Scan(&c.path, &c.kind, &c.state, &c.deadline, &c.reporting); e != nil {
			_ = copies.Close()
			return e
		}
		list = append(list, c)
	}
	if e = copies.Close(); e != nil {
		return e
	}
	var cleanupErr error
	for _, c := range list {
		if c.kind == "backup" && c.state != "building" && c.deadline > now {
			continue
		}
		if c.kind == "backup" || c.kind == "staging" || c.state == "building" {
			if e = s.destroyCopy(ctx, c.path); e != nil {
				cleanupErr = errors.Join(cleanupErr, e)
			}
			continue
		}
		if e = privatePath(c.path, false); e != nil {
			_, _ = s.privacy.ExecContext(ctx, "UPDATE copies SET state='quarantined' WHERE path=?", c.path)
			cleanupErr = errors.Join(cleanupErr, ErrPrivacy)
			continue
		}
		d, e := analyticsdataset.OpenProduction(ctx, c.path, s.binding, false)
		if e == nil {
			if d.ProductionReportingUnix() != c.reporting {
				e = ErrPrivacy
			} else {
				e = d.MaintainProduction(ctx, now, a.Generation, revoked)
			}
			e = errors.Join(e, d.Close())
		}
		if e != nil {
			_, _ = s.privacy.ExecContext(ctx, "UPDATE copies SET state='quarantined' WHERE path=?", c.path)
			cleanupErr = errors.Join(cleanupErr, ErrPrivacy)
			continue
		}
		if _, e = s.privacy.ExecContext(ctx, "UPDATE copies SET state='ready',generation=?,revision=revision+1 WHERE path=?", a.Generation, c.path); e != nil {
			return e
		}
	}
	// Closed receipts survive only while supporting retained observations or
	// monthly permission provenance. The current objection lives in controls.
	if e = s.pruneReceipts(ctx); e != nil {
		return e
	}
	if e = reclaimSQL(ctx, s.journal); e != nil {
		return e
	}
	if s.appDirty.Swap(false) || len(revoked) > 0 {
		pool, err := s.app.DB()
		if err != nil {
			return err
		}
		if e = reclaimSQL(ctx, pool); e != nil {
			s.appDirty.Store(true)
			return e
		}
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	for _, id := range revoked {
		var copies int
		if e = s.privacy.QueryRowContext(ctx, "SELECT count(*) FROM backup_subjects b JOIN copies c ON c.path=b.path WHERE b.subject=? AND c.state!='deleted'", id).Scan(&copies); e != nil {
			return e
		}
		status := "complete"
		if copies > 0 {
			status = "backup_pending"
		}
		if _, e = s.privacy.ExecContext(ctx, "UPDATE links SET cleanup=? WHERE subject=?", status, id); e != nil {
			return e
		}
	}
	if e = s.reconcileObjections(ctx); e != nil {
		return e
	}

	if e = s.app.WithContext(ctx).Exec("DELETE FROM analytics_gaps WHERE end_unix<?", now-90*86400).Error; e != nil {
		return e
	}
	if e = s.retireCompletedControls(ctx); e != nil {
		return e
	}

	if _, e = s.privacy.ExecContext(ctx, "UPDATE privacy_meta SET maintenance=? WHERE singleton=1", now); e != nil {
		return e
	}
	return s.writeAnchor(ctx)
}
func (s *Service) destroyCopy(ctx context.Context, path string) error {
	// Paths come only from the operator registry and are re-confined before unlink.
	if e := s.confined(path); e != nil {
		return e
	}
	for _, p := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		i, e := os.Lstat(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil || !i.Mode().IsRegular() || i.Mode()&os.ModeSymlink != 0 {
			return ErrPrivacy
		}
		if e = os.Remove(p); e != nil {
			return ErrPrivacy
		}
	}
	tx, e := s.privacy.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	if _, e = tx.ExecContext(ctx, "DELETE FROM backup_subjects WHERE path=?", path); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE copies SET state='deleted' WHERE path=?", path); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	return s.writeAnchor(ctx)
}
