package newsletter

import (
	"context"
	"errors"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

var ErrPhysicalCleanupPending = errors.New("newsletter physical cleanup pending")

// Maintain runs bounded logical work before checkpointing active files. A
// physical busy/failure keeps capture paused while healthy proofs can still run.
func (s *Service) Maintain(ctx context.Context, j *store.LifecycleJournal, pending, withdrawn config.NewsletterRetention, opts store.AccountDeletionOptions) error {
	if !s.valid() || j == nil {
		return ErrUnavailable
	}
	key, err := store.DeriveNewsletterIdentityKey(s.RateKey)
	if err != nil {
		return err
	}
	bound, err := s.Store.ConfigureNewsletterIdentityIfBound(ctx, key)
	if err != nil {
		return err
	}
	if !bound {
		return s.Sweep(ctx, j, pending, withdrawn, opts)
	}
	if !pending.NoExpiry || !withdrawn.NoExpiry {
		return ErrUnavailable
	}
	if err := j.ReconcileNewsletterIdentityIntents(ctx); err != nil {
		return err
	}
	if err := j.ValidateNewsletterIdentities(ctx); err != nil {
		return err
	}
	if err := s.expireAccountWork(ctx, j); err != nil {
		return err
	}
	if err := s.Minimise(ctx, j); err != nil {
		return err
	}
	if err := j.CompactNewsletterIdentityCheckpoints(ctx); err != nil {
		return err
	}
	if err := j.CleanupNewsletterFiles(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrPhysicalCleanupPending
	}
	return nil
}

func (s *Service) expireAccountWork(ctx context.Context, j *store.LifecycleJournal) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.Store.AccountWork.Lock()
		if err := j.CheckReady(ctx); err != nil {
			s.Store.AccountWork.Unlock()
			return err
		}
		more := false
		err := s.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var err error
			more, err = mailqueue.SweepAccountProofSlice(ctx, tx, s.now())
			return err
		})
		s.Store.AccountWork.Unlock()
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
}
