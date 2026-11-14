package analytics

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"
)

func (s *Service) pruneReceipts(ctx context.Context) error {
	type authority struct {
		epoch, subject string
		revision       int64
		history        bool
	}
	rows, e := s.journal.QueryContext(ctx, `SELECT p.id,p.subject,p.revision,
 EXISTS(SELECT 1 FROM facts f WHERE f.epoch=p.id) OR EXISTS(
 SELECT 1 FROM contributions c WHERE c.subject=p.subject
 AND c.month>=strftime('%Y-%m',p.start,'unixepoch')
 AND c.month<=strftime('%Y-%m',coalesce(p.end-1,?),'unixepoch')) FROM processing_periods p`, s.now().Unix())
	if e != nil {
		return e
	}
	var periods []authority
	for rows.Next() {
		var a authority
		if e = rows.Scan(&a.epoch, &a.subject, &a.revision, &a.history); e != nil {
			_ = rows.Close()
			return e
		}
		periods = append(periods, a)
	}
	if e = errors.Join(rows.Err(), rows.Close()); e != nil {
		return e
	}
	needed := map[string]map[int64]bool{}
	for _, p := range periods {
		if !p.history {
			var pending int64
			if e = s.app.WithContext(ctx).Raw("SELECT count(*) FROM analytics_outbox WHERE epoch=?", p.epoch).Row().Scan(&pending); e != nil {
				return e
			}
			if pending == 0 {
				continue
			}
		}
		var inc string
		e = s.privacy.QueryRowContext(ctx, "SELECT incarnation FROM links WHERE subject=? AND state='active'", p.subject).Scan(&inc)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return e
		}
		if needed[inc] == nil {
			needed[inc] = map[int64]bool{}
		}
		needed[inc][p.revision] = true
	}
	return s.app.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		type receipt struct {
			Incarnation string
			Revision    int64
		}
		var receipts []receipt
		if e := tx.Raw(`SELECT incarnation,revision FROM analytics_receipts r WHERE NOT EXISTS(SELECT 1 FROM analytics_preferences p WHERE p.incarnation=r.incarnation AND p.enabled=1 AND p.revision=r.revision)`).Scan(&receipts).Error; e != nil {
			return e
		}
		for _, r := range receipts {
			if needed[r.Incarnation][r.Revision] {
				continue
			}
			removed := tx.Exec("DELETE FROM analytics_receipts WHERE incarnation=? AND revision=?", r.Incarnation, r.Revision)
			if removed.Error != nil {
				return removed.Error
			}
			if removed.RowsAffected > 0 {
				s.appDirty.Store(true)
			}
		}
		return nil
	})
}
