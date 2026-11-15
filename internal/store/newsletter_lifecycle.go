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

func newsletterContactAuthority(tx *gorm.DB, actor NewsletterContactAuthority) error {
	if actor.UserID <= 0 || !opaqueLifecycleID(actor.Incarnation) || actor.RequestReference == "" || len(actor.RequestReference) > 128 || strings.ContainsAny(actor.RequestReference, "\r\n") {
		return ErrAccountAccess
	}
	_, key, err := EmailIdentity(actor.EmailKey)
	if err != nil || key != actor.EmailKey {
		return ErrAccountAccess
	}
	var count int64
	if err := tx.Raw(`SELECT count(*) FROM users u JOIN account_incarnations a ON a.user_id=u.user_id WHERE u.user_id=? AND u.email_key=? AND u.isNewsletterOnly=1 AND a.incarnation=?`, actor.UserID, actor.EmailKey, actor.Incarnation).Scan(&count).Error; err != nil {
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
		if err := newsletterContactAuthority(tx, actor); err != nil {
			return err
		}
		var user User
		if err := tx.First(&user, actor.UserID).Error; err != nil {
			return err
		}
		var consent []struct {
			Status         string `json:"status"`
			Locale         string `json:"locale"`
			ConsentVersion string `json:"consentVersion"`
			Source         string `json:"source"`
			RequestedUnix  int64  `json:"requestedUnix"`
			ConfirmedUnix  *int64 `json:"confirmedUnix"`
			WithdrawnUnix  *int64 `json:"withdrawnUnix"`
		}
		if err := tx.Table("newsletter_subscriptions").Select("status,locale,consent_version,source,requested_unix,confirmed_unix,withdrawn_unix").Where("user_id=?", actor.UserID).Find(&consent).Error; err != nil {
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
		var err error
		output, err = json.MarshalIndent(map[string]any{"userID": strconv.FormatInt(user.ID, 10), "email": user.Email, "isNewsletterOnly": true, "subscriptions": consent, "proofHistory": proofs}, "", "  ")
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
		if err := RecordNewsletterErasure(ctx, tx, rateKey, user.EmailKey); err != nil {
			return err
		}
		if cleanup != nil {
			return cleanup(tx, user)
		}
		return nil
	}
	return j.deleteAccount(ctx, actor.UserID, operation, now, opts, func(tx *gorm.DB) error { return newsletterContactAuthority(tx, actor) })
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
		if err := RecordNewsletterErasure(ctx, tx, rateKey, user.EmailKey); err != nil {
			return err
		}
		if cleanup != nil {
			return cleanup(tx, user)
		}
		return nil
	}
	return j.deleteAccount(ctx, actor.UserID, operation, now, opts, func(tx *gorm.DB) error {
		if err := newsletterContactAuthority(tx, actor); err != nil {
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
