package auth

import (
	"context"
	"errors"

	"github.com/LowGok/my-chef/internal/newsletter"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func requireAppUser(tx *gorm.DB, userID *int64) error {
	if userID == nil {
		return nil
	}
	var count int64
	if err := tx.Model(&store.User{}).Where("user_id=? AND isNewsletterOnly=0", *userID).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrSession
	}
	return nil
}

func (c *EmailChange) finishNewsletterDestination(ctx context.Context, hash string, destination store.NewsletterContactAuthority) (EmailChangeResult, error) {
	if c.Lifecycle == nil || c.Newsletter == nil || c.Lifecycle.CheckReady(ctx) != nil {
		return EmailChangeResult{}, ErrEmailChangeUnavailable
	}
	now := c.now()
	var saved newsletter.ContactConsent
	var account store.User
	var pending store.PendingEmailChange
	_, err := c.Lifecycle.EraseNewsletterContact(ctx, destination, c.RateKey, now, store.AccountDeletionOptions{
		Fault: c.retirementFault,
		Capture: func(tx *gorm.DB, contact store.User, incarnation string) error {
			proof, change, user, err := loadEmailChange(tx, hash, now)
			if err != nil {
				return err
			}
			switch proof.Purpose {
			case "approve":
				change.CurrentApproved = true
			case "confirm":
				change.ProposedApproved = true
			default:
				return ErrEmailProof
			}
			if change.ProposedKey != destination.EmailKey || !change.ProposedApproved || change.OriginalVerifiedAt != nil && !change.CurrentApproved || cooldownSeconds(now, user.EmailChangedAt) != 0 {
				return ErrEmailProof
			}
			account, pending = user, change
			saved, err = newsletter.ReadContactConsent(tx, contact.ID)
			return err
		},
		Cleanup: func(tx *gorm.DB, contact store.User) error {
			return store.RecordNewsletterErasure(ctx, tx, c.RateKey, account.EmailKey)
		},
		AfterDelete: func(tx *gorm.DB, contact store.User) error {
			if err := c.finishChange(ctx, tx, now, account, pending); err != nil {
				return err
			}
			account.Email, account.EmailKey = pending.ProposedEmail, pending.ProposedKey
			return c.Newsletter.TransferContactConsent(ctx, tx, now, saved, account)
		},
	})
	if err != nil {
		if errors.Is(err, store.ErrAccountAccess) {
			return EmailChangeResult{}, ErrEmailProof
		}
		return EmailChangeResult{}, changeError(err)
	}
	return EmailChangeResult{Status: "changed", UserID: account.ID}, nil
}
