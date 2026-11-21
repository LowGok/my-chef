package newsletter

import (
	"context"
	"errors"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

// Minimise removes eligible purpose-owned plaintext and marks active-file
// maintenance pending. It never certifies physical cleanup or deletes an ID.
func (s *Service) Minimise(ctx context.Context, j *store.LifecycleJournal) error {
	if !s.valid() || j == nil {
		return ErrUnavailable
	}
	after := int64(0)
	for {
		last, count, err := s.minimiseSlice(ctx, j, after)
		if err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		after = last
	}
}
func (s *Service) minimiseSlice(ctx context.Context, j *store.LifecycleJournal, after int64) (int64, int, error) {
	s.Store.AccountWork.Lock()
	defer s.Store.AccountWork.Unlock()
	if err := ctx.Err(); err != nil {
		return after, 0, err
	}
	if err := j.CheckReady(ctx); err != nil {
		return after, 0, ErrUnavailable
	}
	now := s.now()
	if err := s.expireTransientOwned(ctx, now); err != nil {
		return after, 0, err
	}
	var users []store.User
	if err := s.Store.DB.WithContext(ctx).Where("isNewsletterOnly=1 AND user_id>? AND (user_id NOT IN (SELECT user_id FROM newsletter_subscriptions) OR user_id IN (SELECT user_id FROM newsletter_subscriptions WHERE status<>'confirmed'))", after).Order("user_id").Limit(100).Find(&users).Error; err != nil {
		return after, 0, err
	}
	for _, user := range users {
		if err := s.minimiseContact(ctx, j, user, now); err != nil {
			return after, len(users), err
		}
		after = user.ID
	}
	return after, len(users), nil
}

func (s *Service) contactAuthorityLive(ctx context.Context, tx *gorm.DB, user store.User, now time.Time) (bool, error) {
	if user.NewsletterMinimised {
		return false, nil
	}
	signup, err := store.EmailRateBucket(s.RateKey, "signup-proof/address", user.EmailKey)
	if err != nil {
		return false, err
	}
	recovery, err := store.EmailRateBucket(s.RateKey, "recovery-job/address", user.EmailKey)
	if err != nil {
		return false, err
	}
	var count int64
	err = tx.WithContext(ctx).Raw(`SELECT
 (SELECT count(*) FROM newsletter_proofs WHERE user_id=? AND purpose='confirm' AND consumed_unix IS NULL AND expires_unix>?) +
 (SELECT count(*) FROM signup_proofs WHERE address_tag=? AND consumed IS NULL AND expires>?) +
 (SELECT count(*) FROM email_verification_proofs WHERE user_id=? AND consumed IS NULL AND expires>?) +
 (SELECT count(*) FROM pending_email_changes WHERE (user_id=? OR proposed_key=? OR original_key=?) AND expires>?) +
 (SELECT count(*) FROM recovery_proofs WHERE user_id=? AND consumed_ns IS NULL AND expires_ns>?) +
 (SELECT count(*) FROM recovery_jobs WHERE address_tag=? AND expires_ns>?) +
 (SELECT count(*) FROM email_outbox WHERE (recipient=? OR id IN (SELECT outbox_id FROM account_mail_owners m JOIN account_incarnations a ON a.incarnation=m.incarnation WHERE a.user_id=?))
  AND state IN ('queued','sending') AND created_at>? AND (NOT json_valid(data) OR COALESCE(json_extract(data,'$.expires'),?)>?))`, user.ID, now.Unix(), signup, now.Unix(), user.ID, now.Unix(), user.ID, user.EmailKey, user.EmailKey, now.Unix(), user.ID, now.UnixNano(), recovery, now.UnixNano(), user.Email, user.ID, now.Add(-23*time.Hour).Unix(), now.Add(time.Second).Unix(), now.Unix()).Scan(&count).Error
	return count > 0, err
}

func (s *Service) minimiseContact(ctx context.Context, j *store.LifecycleJournal, user store.User, now time.Time) error {
	current, found, err := j.NewsletterIdentity(ctx, user.ID)
	if err != nil || !found || current.Class != "contact" {
		return store.ErrLifecycleUncertain
	}
	var sub subscription
	if err := s.Store.DB.WithContext(ctx).Take(&sub, "user_id=?", user.ID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		sub.Status = "pending"
	} else if err != nil {
		return err
	}
	if sub.Status != current.Status || sub.Revision != current.Revision || sub.ProvedOrder != current.DecisionOrder {
		return store.ErrLifecycleConflict
	}
	live, err := s.contactAuthorityLive(ctx, s.Store.DB, user, now)
	if err != nil || live {
		return err
	}
	if user.NewsletterMinimised {
		return s.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return s.eraseInactiveReferences(ctx, tx, user.ID, current.Incarnation, now) })
	}
	reduced := current
	reduced.Minimised = true
	return j.TransitionNewsletterIdentity(ctx, reduced, func(tx *gorm.DB) error {
		// Recheck the complete current post-state, including profile and authority,
		// before acknowledging this journal transition. SQL validation rolls back an
		// ambiguous profile instead of discarding it.
		live, err := s.contactAuthorityLive(ctx, tx, user, now)
		if err != nil {
			return err
		}
		if live {
			return store.ErrLifecycleConflict
		}
		if err := s.eraseInactiveReferences(ctx, tx, user.ID, current.Incarnation, now); err != nil {
			return err
		}
		if err := tx.Create(&store.NewsletterIdentityMatch{UserID: user.ID, Incarnation: current.Incarnation, KeyVersion: current.KeyVersion, AddressTag: current.AddressTag}).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE users SET newsletter_minimised=1,email=NULL,email_key=NULL,username=NULL WHERE user_id=? AND isNewsletterOnly=1 AND newsletter_minimised=0", user.ID).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE newsletter_subscriptions SET email_key=NULL,locale=NULL,source=NULL,requested_unix=NULL,confirmed_unix=NULL,decision_unix=? WHERE user_id=?", current.DecisionUnix, user.ID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE newsletter_proofs SET email_key=NULL,identity_incarnation=?,identity_key_version=?,identity_address_tag=? WHERE user_id=? AND purpose='unsubscribe'`, current.Incarnation, current.KeyVersion, current.AddressTag, user.ID).Error; err != nil {
			return err
		}
		return tx.Exec("UPDATE newsletter_identity_namespace SET physical_cleanup_pending=1 WHERE singleton=1").Error
	})
}

func (s *Service) eraseInactiveReferences(ctx context.Context, tx *gorm.DB, userID int64, incarnation string, now time.Time) error {
	// Expired confirmations and obsolete withdrawal receipts carry no remaining
	// authority. Keep every unconsumed withdrawal token, regardless of its age.
	var proofs []proofRecord
	if err := tx.WithContext(ctx).Where("user_id=? AND (purpose='confirm' AND (expires_unix<=? OR consumed_unix IS NOT NULL) OR purpose='unsubscribe' AND consumed_unix<=?)", userID, now.Unix(), now.Add(-24*time.Hour).Unix()).Find(&proofs).Error; err != nil {
		return err
	}
	for _, proof := range proofs {
		if err := mailqueue.InvalidateProtected(ctx, tx, now, "newsletter/"+proof.Hash); err != nil {
			return err
		}
		if err := tx.Delete(&proof).Error; err != nil {
			return err
		}
	}
	var rows []mailqueue.Record
	if err := tx.Where("id IN (SELECT outbox_id FROM account_mail_owners WHERE incarnation=?)", incarnation).Find(&rows).Error; err != nil {
		return err
	}
	changed := len(proofs) > 0
	for _, row := range rows {
		if row.State == "queued" || row.State == "sending" {
			var active int64
			if err := tx.Raw("SELECT count(*) FROM newsletter_proofs WHERE ('newsletter/'||hash)=? AND consumed_unix IS NULL AND (expires_unix IS NULL OR expires_unix>?)", row.Key, now.Unix()).Scan(&active).Error; err != nil {
				return err
			}
			if active != 0 {
				continue
			}
			// A non-newsletter live job was excluded before this helper. During a
			// reduced retry, leave any newly issued temporary job to its own expiry.
			if row.Template != mailqueue.NewsletterENTemplate && row.Template != mailqueue.NewsletterFRTemplate && row.Template != mailqueue.NewsletterNLTemplate && row.CreatedAt > now.Add(-23*time.Hour).Unix() {
				continue
			}
		}
		if row.ProviderID != nil {
			if err := tx.Exec("DELETE FROM email_events WHERE provider_id=?", *row.ProviderID).Error; err != nil {
				return err
			}
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
		changed = true
	}
	if changed {
		return tx.Exec("UPDATE newsletter_identity_namespace SET physical_cleanup_pending=1 WHERE singleton=1").Error
	}
	return nil
}

// withProofTransition journals a tracked newsletter identity around its SQL
// decision. Ordinary accounts without that origin keep the legacy lifecycle.
func (s *Service) withProofTransition(ctx context.Context, proof proofRecord, purpose string, apply func(*gorm.DB, int64, *int64) error) error {
	var record store.NewsletterIdentityRecord
	tracked := false
	var err error
	if s.Lifecycle != nil {
		record, tracked, err = s.Lifecycle.NewsletterIdentity(ctx, proof.UserID)
		if err != nil {
			return err
		}
	}
	if !tracked {
		return s.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			order, err := nextOrder(tx)
			if err != nil {
				return err
			}
			return apply(tx, order, nil)
		})
	}
	var sub subscription
	if err := s.Store.DB.WithContext(ctx).Take(&sub, "user_id=?", proof.UserID).Error; err != nil {
		return err
	}
	if _, err := s.proofOwner(ctx, s.Store.DB.WithContext(ctx), proof); err != nil {
		return err
	}
	if proof.ConsumedUnix != nil {
		if purpose == "unsubscribe" {
			return nil
		}
		return ErrProof
	}
	if proof.Revision != sub.Revision || proof.ExpiresUnix != nil && *proof.ExpiresUnix <= s.now().Unix() {
		return ErrProof
	}
	var order int64
	if err := s.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error { var err error; order, err = nextOrder(tx); return err }); err != nil {
		return err
	}
	record.DecisionOrder = order
	record.DecisionUnix = store.Ptr(s.now().Unix())
	if purpose == "unsubscribe" {
		record.Status = "unsubscribed"
		record.Revision++
	} else {
		var proposed proposal
		if err := s.open("newsletter-proposal", proofBinding(proof), proof.Proposal, &proposed); err != nil {
			return err
		}
		record.Status = "confirmed"
		record.Minimised = false
		record.NoticeVersion = store.Ptr(proposed.ConsentVersion)
	}
	return s.Lifecycle.TransitionNewsletterIdentity(ctx, record, func(tx *gorm.DB) error { return apply(tx, order, record.DecisionUnix) })
}

func (s *Service) proofOwner(ctx context.Context, tx *gorm.DB, proof proofRecord) (store.User, error) {
	var user store.User
	if err := tx.WithContext(ctx).First(&user, proof.UserID).Error; err != nil {
		return user, err
	}
	if proof.IdentityIncarnation == nil {
		if user.EmailKey != proof.EmailKey || user.NewsletterMinimised {
			return user, ErrProof
		}
		return user, nil
	}
	var incarnation string
	if err := tx.Raw("SELECT incarnation FROM account_incarnations WHERE user_id=?", user.ID).Row().Scan(&incarnation); err != nil || incarnation != *proof.IdentityIncarnation || proof.IdentityKeyVersion == nil || proof.IdentityAddressTag == nil {
		return user, ErrProof
	}
	key, err := store.DeriveNewsletterIdentityKey(s.RateKey)
	if err != nil || key.Version() != *proof.IdentityKeyVersion {
		return user, ErrProof
	}
	if user.NewsletterMinimised {
		var match store.NewsletterIdentityMatch
		if err := tx.Take(&match, "user_id=?", user.ID).Error; err != nil || match.Incarnation != incarnation || match.AddressTag != *proof.IdentityAddressTag || match.KeyVersion != *proof.IdentityKeyVersion {
			return user, ErrProof
		}
	} else {
		tag, err := key.Tag(user.Email)
		if err != nil || tag != *proof.IdentityAddressTag {
			return user, ErrProof
		}
	}
	return user, nil
}
