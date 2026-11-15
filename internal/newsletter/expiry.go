package newsletter

import (
	"context"
	"errors"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

// Sweep removes only expired contact identities. The deletion transaction checks
// the same consent revision and expiry so a concurrent confirmation is preserved.
func (s *Service) Sweep(ctx context.Context, journal *store.LifecycleJournal, pending, withdrawn config.NewsletterRetention, opts store.AccountDeletionOptions) error {
	if s == nil || s.Store == nil || journal == nil || len(s.RateKey) != 32 || !pending.Valid() || !withdrawn.Valid() {
		return ErrUnavailable
	}
	now := s.now()
	if err := s.expireTransient(ctx, now); err != nil {
		return err
	}
	if pending.NoExpiry && withdrawn.NoExpiry {
		return nil
	}
	var candidates []struct {
		UserID                        int64
		EmailKey, Incarnation, Status string
		Revision                      int64
	}
	pendingCutoff := now.Add(-time.Duration(pending.Days) * 24 * time.Hour).Unix()
	withdrawnCutoff := now.Add(-time.Duration(withdrawn.Days) * 24 * time.Hour).Unix()
	err := s.Store.DB.WithContext(ctx).Raw(`SELECT u.user_id,u.email_key,a.incarnation,n.status,n.revision
 FROM users u JOIN account_incarnations a ON a.user_id=u.user_id JOIN newsletter_subscriptions n ON n.user_id=u.user_id
 WHERE u.isNewsletterOnly=1 AND ((? AND n.status='pending' AND n.requested_unix<=?) OR (? AND n.status='unsubscribed' AND n.withdrawn_unix<=?))
 ORDER BY u.user_id LIMIT 100`, !pending.NoExpiry, pendingCutoff, !withdrawn.NoExpiry, withdrawnCutoff).Scan(&candidates).Error
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		cutoff := pendingCutoff
		if candidate.Status == "unsubscribed" {
			cutoff = withdrawnCutoff
		}
		actor := store.NewsletterContactAuthority{UserID: candidate.UserID, EmailKey: candidate.EmailKey, Incarnation: candidate.Incarnation, RequestReference: "configured-newsletter-retention"}
		_, err := journal.ExpireNewsletterContact(ctx, actor, candidate.Status, candidate.Revision, cutoff, s.RateKey, now, opts)
		if err != nil && !errors.Is(err, store.ErrAccountAccess) {
			return err
		}
	}
	return nil
}

func (s *Service) expireTransient(ctx context.Context, now time.Time) error {
	s.Store.AccountWork.RLock()
	defer s.Store.AccountWork.RUnlock()
	if s.Lifecycle != nil && s.Lifecycle.CheckReady(ctx) != nil {
		return ErrUnavailable
	}
	return s.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM newsletter_requests WHERE expires_unix<=?", now.Unix()).Error; err != nil {
			return err
		}
		var expired []proofRecord
		if err := tx.Where("purpose='confirm' AND expires_unix<=? AND (consumed_unix IS NULL OR proposal<>'')", now.Unix()).Order("created_unix,hash").Limit(100).Find(&expired).Error; err != nil {
			return err
		}
		for _, proof := range expired {
			if err := mailqueue.InvalidateProtected(ctx, tx, now, "newsletter/"+proof.Hash); err != nil {
				return err
			}
			if err := tx.Model(&proof).Updates(map[string]any{"consumed_unix": gorm.Expr("COALESCE(consumed_unix,?)", now.Unix()), "proposal": ""}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) RunSweep(ctx context.Context, journal *store.LifecycleJournal, pendingDays, withdrawnDays config.NewsletterRetention, opts store.AccountDeletionOptions) error {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := s.Sweep(ctx, journal, pendingDays, withdrawnDays, opts); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
