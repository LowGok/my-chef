package analytics

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const WorkerInterval = time.Minute

type Health struct {
	Code          string `json:"code"`
	Queued        int64  `json:"queued"`
	OldestSeconds int64  `json:"oldestSeconds"`
	CapturePaused bool   `json:"capturePaused"`
}

func (s *Service) collectionReady() bool {
	return !s.unhealthy.Load() && s.config.Authorized(s.now()) && len(s.backupKey) == 32
}
func (s *Service) Health(ctx context.Context) Health {
	h := Health{Code: "healthy", CapturePaused: !s.collectionReady()}
	if s.unhealthy.Load() {
		h.Code = "privacy_or_delivery_unavailable"
	}
	var oldest sql.NullInt64
	if e := s.app.WithContext(ctx).Raw("SELECT count(*),min(occurred_unix) FROM analytics_outbox").Row().Scan(&h.Queued, &oldest); e != nil {
		h.Code = "app_storage_unavailable"
		h.CapturePaused = true
	}
	if oldest.Valid {
		h.OldestSeconds = max(0, s.now().Unix()-oldest.Int64)
	}
	return h
}
func (s *Service) Run(ctx context.Context, observe func(Health)) error {
	timer := time.NewTicker(WorkerInterval)
	defer timer.Stop()
	for {
		call, cancel := context.WithTimeout(ctx, 50*time.Second)
		e := s.Tick(call)
		if e != nil && ctx.Err() == nil {
			s.unhealthy.Store(true)
			s.degraded.Store(true)
		}
		if observe != nil {
			observe(s.Health(call))
		}
		cancel()
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
	}
}
func (s *Service) Tick(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			s.unhealthy.Store(true)
			s.degraded.Store(true)
		}
	}()
	if err = s.Maintain(ctx); err != nil {
		return err
	}
	s.unhealthy.Store(false)
	if !s.collectionReady() {
		return nil
	}
	if err = s.Deliver(ctx, nil); err != nil {
		return err
	}
	if e := s.mu.LockContext(ctx); e != nil {
		return e
	}
	defer s.mu.Unlock()
	if err = s.heartbeatLocked(ctx); err != nil {
		return err
	}
	var latest sql.NullInt64
	if err = s.privacy.QueryRowContext(ctx, "SELECT max(reporting) FROM copies WHERE kind='extract' AND state='ready'").Scan(&latest); err != nil {
		return err
	}
	if !latest.Valid || latest.Int64 < monthDayStart(s.now().Unix()) {
		_, err = s.refreshLocked(ctx)
	}
	return err
}
func monthDayStart(at int64) int64 {
	t := time.Unix(at, 0).UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Unix()
}
func (s *Service) heartbeatLocked(ctx context.Context) error {
	now := s.now().Unix()
	start := now
	if s.lastHeartbeat > 0 {
		start = s.lastHeartbeat + 1
	}
	if start > now {
		start = now
	}
	status := "complete"
	if s.degraded.Swap(false) || s.lastHeartbeat > 0 && now-s.lastHeartbeat > 120 {
		status = "partial"
	}
	var queued, gaps int64
	if e := s.app.WithContext(ctx).Raw("SELECT count(*) FROM analytics_outbox").Row().Scan(&queued); e != nil {
		return e
	}
	if e := s.app.WithContext(ctx).Raw("SELECT count(*) FROM analytics_gaps WHERE start_unix<? AND end_unix>?", now+1, start).Row().Scan(&gaps); e != nil {
		return e
	}
	if queued > 0 || gaps > 0 {
		status = "partial"
	}
	type pRow struct {
		Epoch    string
		Sequence int64
	}
	var rows []pRow
	if e := s.app.WithContext(ctx).Raw("SELECT epoch,sequence FROM analytics_preferences WHERE enabled=1").Scan(&rows).Error; e != nil {
		return e
	}
	for _, p := range rows {
		var sequence int64
		var contiguous bool
		e := s.journal.QueryRowContext(ctx, "SELECT sequence,contiguous FROM source_checkpoints WHERE epoch=?", p.Epoch).Scan(&sequence, &contiguous)
		if errors.Is(e, sql.ErrNoRows) && p.Sequence == 0 {
			continue
		}
		if e != nil || !contiguous || sequence != p.Sequence {
			status = "partial"
		}
	}
	if _, e := s.journal.ExecContext(ctx, "INSERT INTO coverage VALUES(?,?,?,'activity') ON CONFLICT(start,source) DO UPDATE SET end=max(end,excluded.end),status=CASE WHEN status='complete' AND excluded.status='complete' THEN 'complete' ELSE 'partial' END", start, now+1, status); e != nil {
		return e
	}
	s.lastHeartbeat = now
	return nil
}
func (s *Service) Current(ctx context.Context) (string, error) {
	if e := s.mu.LockContext(ctx); e != nil {
		return "", e
	}
	defer s.mu.Unlock()
	if e := s.checkAnchor(ctx); e != nil {
		return "", e
	}
	b, e := os.ReadFile(filepath.Join(s.root, "current"))
	if e != nil {
		return "", ErrPrivacy
	}
	path := string(b)
	if e = s.registered(ctx, path); e != nil {
		return "", e
	}
	return path, nil
}
