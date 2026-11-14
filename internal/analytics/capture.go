package analytics

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

type requestCapture struct {
	service *Service
	capture Capture
	user    int64
}

func (s *Service) Request(ctx context.Context, user, revision int64, platform string) context.Context {
	c := Capture{Revision: revision, Platform: platform}
	inc, e := incarnationOn(s.app.WithContext(ctx), user)
	if e == nil {
		p, e := preferenceOn(s.app.WithContext(ctx), inc)
		if e == nil && p.Enabled && p.Revision == revision {
			c.Epoch = p.Epoch
		}
	}
	return context.WithValue(ctx, captureKey{}, requestCapture{s, c, user})
}
func CaptureEvent(ctx context.Context, tx *gorm.DB, user int64, name, receipt string) error {
	r, ok := ctx.Value(captureKey{}).(requestCapture)
	if !ok || r.service == nil {
		return nil
	}
	s := r.service
	if !s.collectionReady() {
		return nil
	}
	inc, e := incarnationOn(tx, user)
	if e != nil {
		return nil
	}
	p, e := preferenceOn(tx, inc)
	if e != nil {
		return e
	}
	if !p.Enabled {
		return nil
	}
	gap := func() error {
		s.degraded.Store(true)
		if e := tx.Exec("UPDATE analytics_preferences SET capture_gap=1 WHERE incarnation=?", inc).Error; e != nil {
			return e
		}
		return tx.Exec("INSERT INTO analytics_gaps VALUES(?,?,'capture_gap') ON CONFLICT(start_unix,code) DO UPDATE SET end_unix=max(end_unix,excluded.end_unix)", s.now().Unix(), s.now().Unix()+1).Error
	}
	if r.user != user || p.Revision != r.capture.Revision || p.Epoch != r.capture.Epoch || r.capture.Epoch == "" || p.NoticeVersion != s.config.NoticeVersion || p.BasisVersion != s.config.Assessment.Version {
		return gap()
	}
	active, e := s.authorizedLink(ctx, inc, p)
	if e != nil || !active || s.checkAnchor(ctx) != nil {
		return gap()
	}

	var count int64
	if e = tx.Raw("SELECT count(*) FROM analytics_outbox").Row().Scan(&count); e != nil {
		return e
	}
	cap := s.config.OutboxCap
	if cap <= 0 {
		cap = 10000
	}
	if count >= int64(cap) {
		return gap()
	}
	if receipt == "" {
		receipt, e = NewID("ded")
		if e != nil {
			return gap()
		}
	}
	// The domain transaction already owns the app writer. No external lock is taken here.
	// Recover only optional writes; a failure to persist the gap remains a product storage error.
	if e = tx.SavePoint("analytics_capture").Error; e != nil {
		return gap()
	}
	enqueue := func() error {
		var prior int64
		if e = tx.Raw("SELECT count(*) FROM analytics_outbox WHERE incarnation=? AND epoch=? AND receipt=? AND name=?", inc, p.Epoch, receipt, name).Row().Scan(&prior); e != nil {
			return e
		}
		if prior > 0 {
			return nil
		}
		id, e := s.uniqueEventID(ctx, tx)
		if e != nil {
			return e
		}
		event := Event{Version: 2, ID: id, Subject: p.Subject, Epoch: p.Epoch, Occurred: s.now().Unix(), Name: name, Platform: r.capture.Platform, Plan: "free", Sequence: p.Sequence + 1}
		// Premium fixture grants are never promoted into production analytical facts.
		if e = event.Validate(); e != nil {
			return e
		}
		if e = tx.Exec("INSERT INTO analytics_outbox VALUES(?,?,?,?,?,?,?,?,?,?)", id, inc, event.Subject, event.Epoch, event.Sequence, event.Occurred, event.Name, event.Platform, event.Plan, receipt).Error; e != nil {
			return e
		}
		return tx.Exec("UPDATE analytics_preferences SET sequence=sequence+1 WHERE incarnation=?", inc).Error
	}
	if e = enqueue(); e != nil {
		if rollback := tx.RollbackTo("analytics_capture").Error; rollback != nil {
			return errors.Join(e, rollback)
		}
		if e = gap(); e != nil {
			return e
		}
	}
	return tx.Exec("RELEASE SAVEPOINT analytics_capture").Error
}

// SaveGenerationCapture records the admission epoch for asynchronous work.
func SaveGenerationCapture(ctx context.Context, tx *gorm.DB, id int64) error {
	r, ok := ctx.Value(captureKey{}).(requestCapture)
	if !ok || r.service == nil || !r.service.collectionReady() {
		return nil
	}
	if e := tx.SavePoint("analytics_admission").Error; e != nil {
		return e
	}
	e := tx.Exec("INSERT OR IGNORE INTO analytics_generation VALUES(?,?,?,?)", id, r.capture.Revision, r.capture.Platform, r.capture.Epoch).Error
	if e != nil {
		if e = tx.RollbackTo("analytics_admission").Error; e != nil {
			return e
		}
		r.service.degraded.Store(true)
		if e = tx.Exec("INSERT INTO analytics_gaps VALUES(?,?,'capture_gap') ON CONFLICT(start_unix,code) DO UPDATE SET end_unix=max(end_unix,excluded.end_unix)", r.service.now().Unix(), r.service.now().Unix()+1).Error; e != nil {
			return e
		}
	}
	return tx.Exec("RELEASE SAVEPOINT analytics_admission").Error
}
func (s *Service) GenerationContext(ctx context.Context, id, user int64) context.Context {
	var c Capture
	r := s.app.WithContext(ctx).Raw("SELECT revision,platform,epoch FROM analytics_generation WHERE operation_id=?", id).Scan(&c)
	if r.Error != nil || r.RowsAffected != 1 {
		return ctx
	}
	return context.WithValue(ctx, captureKey{}, requestCapture{s, c, user})
}
func OperationReceipt(kind string, id int64) string { return fmt.Sprintf("%s:%d", kind, id) }
