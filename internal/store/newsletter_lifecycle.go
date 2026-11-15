package store

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// NewsletterContactAuthority is supplied only by the service operator after
// mailbox verification. Public HTTP handlers never accept this authority.
type NewsletterContactAuthority struct {
	UserID           int64  `json:"-"`
	EmailKey         string `json:"emailKey"`
	Incarnation      string `json:"incarnation"`
	RequestReference string `json:"requestReference"`
}

func (s *Store) newsletterContactAuthority(tx *gorm.DB, actor NewsletterContactAuthority) error {
	if actor.UserID <= 0 || !opaqueLifecycleID(actor.Incarnation) || actor.RequestReference == "" || len(actor.RequestReference) > 128 || strings.ContainsAny(actor.RequestReference, "\r\n") {
		return ErrAccountAccess
	}
	_, key, err := EmailIdentity(actor.EmailKey)
	if err != nil || key != actor.EmailKey {
		return ErrAccountAccess
	}
	owner, found, err := s.ResolveEmailOwnerTx(tx.Statement.Context, tx, actor.EmailKey)
	if err != nil {
		return err
	}
	if !found || owner.ID != actor.UserID || !owner.IsNewsletterOnly {
		return ErrAccountAccess
	}
	var count int64
	if err := tx.Raw(`SELECT count(*) FROM account_incarnations WHERE user_id=? AND incarnation=?`, actor.UserID, actor.Incarnation).Scan(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrAccountAccess
	}
	return nil
}

func (s *Store) ExportNewsletterContact(ctx context.Context, actor NewsletterContactAuthority) ([]byte, error) {
	s.AccountWork.RLock()
	defer s.AccountWork.RUnlock()
	var output []byte
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.newsletterContactAuthority(tx, actor); err != nil {
			return err
		}
		var user User
		if err := tx.First(&user, actor.UserID).Error; err != nil {
			return err
		}
		var consent []struct {
			Status         string  `json:"status"`
			Locale         *string `json:"locale"`
			ConsentVersion string  `json:"consentVersion"`
			Source         *string `json:"source"`
			RequestedUnix  *int64  `json:"requestedUnix"`
			ConfirmedUnix  *int64  `json:"confirmedUnix"`
			WithdrawnUnix  *int64  `json:"withdrawnUnix"`
			DecisionUnix   *int64  `json:"decisionUnix"`
		}
		if err := tx.Table("newsletter_subscriptions").Select("status,locale,consent_version,source,requested_unix,confirmed_unix,withdrawn_unix,decision_unix").Where("user_id=?", actor.UserID).Find(&consent).Error; err != nil {
			return err
		}
		var proofs []struct {
			Purpose      string `json:"purpose"`
			CreatedUnix  int64  `json:"createdUnix"`
			ExpiresUnix  *int64 `json:"expiresUnix"`
			ConsumedUnix *int64 `json:"consumedUnix"`
		}
		if err := tx.Table("newsletter_proofs").Select("purpose,created_unix,expires_unix,consumed_unix").Where("user_id=?", actor.UserID).Order("created_unix,purpose").Find(&proofs).Error; err != nil {
			return err
		}
		var storedEmail *string
		if user.Email != "" {
			storedEmail = Ptr(user.Email)
		}
		var err error
		output, err = json.MarshalIndent(map[string]any{"userID": strconv.FormatInt(user.ID, 10), "email": storedEmail, "isNewsletterOnly": true, "newsletterMinimised": user.NewsletterMinimised, "verification": map[string]string{"suppliedEmail": actor.EmailKey, "requestReference": actor.RequestReference}, "subscriptions": consent, "proofHistory": proofs}, "", "  ")
		return err
	})
	return output, err
}

func (j *LifecycleJournal) EraseNewsletterContact(ctx context.Context, actor NewsletterContactAuthority, rateKey []byte, now time.Time, opts AccountDeletionOptions) (AccountDeletion, error) {
	if len(rateKey) != 32 {
		return AccountDeletion{}, ErrAccountAccess
	}
	operation, err := randomLifecycleID()
	if err != nil {
		return AccountDeletion{}, err
	}
	cleanup := opts.Cleanup
	opts.Cleanup = func(tx *gorm.DB, user User) error {
		if err := RecordNewsletterErasure(ctx, tx, rateKey, actor.EmailKey); err != nil {
			return err
		}
		if cleanup != nil {
			return cleanup(tx, user)
		}
		return nil
	}
	result, err := j.deleteAccount(ctx, actor.UserID, operation, now, opts, func(tx *gorm.DB) error { return j.app.newsletterContactAuthority(tx, actor) })
	if err == nil && j.app.newsletterJournalRequired.Load() {
		err = j.CleanupNewsletterFiles(ctx)
	}
	return result, err
}

// ExpireNewsletterContact adds consent-state authority to the normal guarded
// contact erasure. It is used only by configured retention maintenance.
func (j *LifecycleJournal) ExpireNewsletterContact(ctx context.Context, actor NewsletterContactAuthority, status string, revision, cutoff int64, rateKey []byte, now time.Time, opts AccountDeletionOptions) (AccountDeletion, error) {
	if len(rateKey) != 32 || (status != "pending" && status != "unsubscribed") {
		return AccountDeletion{}, ErrAccountAccess
	}
	operation, err := randomLifecycleID()
	if err != nil {
		return AccountDeletion{}, err
	}
	cleanup := opts.Cleanup
	opts.Cleanup = func(tx *gorm.DB, user User) error {
		if err := RecordNewsletterErasure(ctx, tx, rateKey, actor.EmailKey); err != nil {
			return err
		}
		if cleanup != nil {
			return cleanup(tx, user)
		}
		return nil
	}
	return j.deleteAccount(ctx, actor.UserID, operation, now, opts, func(tx *gorm.DB) error {
		if err := j.app.newsletterContactAuthority(tx, actor); err != nil {
			return err
		}
		var count int64
		if err := tx.Raw(`SELECT count(*) FROM newsletter_subscriptions WHERE user_id=? AND status=? AND revision=?
   AND ((status='pending' AND requested_unix<=?) OR (status='unsubscribed' AND withdrawn_unix<=?))`, actor.UserID, status, revision, cutoff, cutoff).Scan(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrAccountAccess
		}
		return nil
	})
}
