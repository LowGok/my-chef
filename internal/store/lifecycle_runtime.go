package store

import (
	"context"
	"time"
)

func (j *LifecycleJournal) RunCleanup(ctx context.Context, cleaner AccountAssetCleaner) error {
	for ctx.Err() == nil {
		did, err := j.CleanupStep(ctx, cleaner, time.Now().UTC())
		if err != nil {
			return err
		}
		if did {
			continue
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}
func (s *Store) RunDeletionMailSweep(ctx context.Context) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	return s.runDeletionMailSweep(ctx, time.Now().UTC(), ticker.C)
}
func (s *Store) runDeletionMailSweep(ctx context.Context, startup time.Time, ticks <-chan time.Time) error {
	if err := s.SweepDeletionMail(ctx, startup); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticks:
			if err := s.SweepDeletionMail(ctx, now); err != nil {
				return err
			}
		}
	}
}
