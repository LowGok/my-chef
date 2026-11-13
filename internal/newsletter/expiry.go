package newsletter

import (
	"context"
	"errors"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

// Sweep removes only expired contact identities. The deletion transaction checks
// the same consent revision and expiry so a concurrent confirmation is preserved.
func (s *Service) Sweep(ctx context.Context, journal *store.LifecycleJournal, pendingDays, withdrawnDays int, opts store.AccountDeletionOptions) error {
	if s == nil || s.Store == nil || journal == nil || len(s.RateKey) != 32 {
		return ErrUnavailable
	}
	now := s.now()
	if err := s.Store.DB.WithContext(ctx).Exec("DELETE FROM newsletter_requests WHERE expires_unix<=?", now.Unix()).Error; err != nil {
		return err
	}
	if pendingDays <= 0 || withdrawnDays <= 0 {
		return nil
	}
	var candidates []struct {
		UserID                        int64
		EmailKey, Incarnation, Status string
		Revision                      int64
	}
	pendingCutoff := now.Add(-time.Duration(pendingDays) * 24 * time.Hour).Unix()
	withdrawnCutoff := now.Add(-time.Duration(withdrawnDays) * 24 * time.Hour).Unix()
	err := s.Store.DB.WithContext(ctx).Raw(`SELECT u.user_id,u.email_key,a.incarnation,n.status,n.revision
 FROM users u JOIN account_incarnations a ON a.user_id=u.user_id JOIN newsletter_subscriptions n ON n.user_id=u.user_id
 WHERE u.isNewsletterOnly=1 AND ((n.status='pending' AND n.requested_unix<=?) OR (n.status='unsubscribed' AND n.withdrawn_unix<=?))
 ORDER BY u.user_id LIMIT 100`, pendingCutoff, withdrawnCutoff).Scan(&candidates).Error
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

func (s *Service) RunSweep(ctx context.Context, journal *store.LifecycleJournal, pendingDays, withdrawnDays int, opts store.AccountDeletionOptions) error {
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
