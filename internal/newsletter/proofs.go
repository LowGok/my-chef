package newsletter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func (s *Service) Confirm(ctx context.Context, token string) error {
	return s.applyProof(ctx, token, "confirm")
}

func (s *Service) Unsubscribe(ctx context.Context, token string) error {
	return s.applyProof(ctx, token, "unsubscribe")
}

func (s *Service) applyProof(ctx context.Context, token, purpose string) error {
	if s == nil || s.Store == nil || purpose == "confirm" && !s.valid() {
		return ErrUnavailable
	}
	s.Store.AccountWork.Lock()
	defer s.Store.AccountWork.Unlock()
	if s.Lifecycle != nil && s.Lifecycle.CheckReady(ctx) != nil {
		return ErrUnavailable
	}
	hash, err := proofHash(token)
	if err != nil {
		return err
	}
	var original proofRecord
	if err := s.Store.DB.WithContext(ctx).Where("hash=? AND purpose=?", hash, purpose).Take(&original).Error; err != nil {
		return ErrProof
	}
	err = s.withProofTransition(ctx, original, purpose, func(tx *gorm.DB, order int64) error {
		now := s.now()
		var proof proofRecord
		if err := tx.Where("hash=? AND purpose=?", hash, purpose).Take(&proof).Error; err != nil {
			return err
		}
		var sub subscription
		if err := tx.Where("user_id=?", proof.UserID).Take(&sub).Error; err != nil {
			return err
		}
		user, err := s.proofOwner(ctx, tx, proof)
		if err != nil {
			return err
		}
		if proof.IdentityIncarnation == nil && !user.NewsletterMinimised && sub.EmailKey != proof.EmailKey {
			return ErrProof
		}
		if proof.ConsumedUnix != nil {
			if purpose == "unsubscribe" {
				return nil
			}
			return ErrProof
		}
		if proof.Revision != sub.Revision || proof.ExpiresUnix != nil && *proof.ExpiresUnix <= now.Unix() {
			return ErrProof
		}
		if purpose == "unsubscribe" {
			sub.Status = "unsubscribed"
			sub.Revision++
			sub.WithdrawnUnix = store.Ptr(now.Unix())
			if err := s.invalidate(ctx, tx, now, proof.UserID); err != nil {
				return err
			}
		} else {
			if proof.RequestOrder <= sub.ProvedOrder {
				return ErrProof
			}
			var proposed proposal
			if err := s.open("newsletter-proposal", proofBinding(proof), proof.Proposal, &proposed); err != nil {
				return err
			}
			_, key, err := store.EmailIdentity(proposed.Email)
			prefix, valid := localePath(proposed.Locale)
			if err != nil || key != proof.EmailKey || !valid || proposed.ConsentVersion == "" {
				return ErrProof
			}
			if user.NewsletterMinimised {
				var nonce [16]byte
				if _, err := rand.Read(nonce[:]); err != nil {
					return err
				}
				if err := tx.Exec("DELETE FROM newsletter_identity_matches WHERE user_id=?", user.ID).Error; err != nil {
					return err
				}
				if err := tx.Exec("UPDATE users SET newsletter_minimised=0,email=?,email_key=?,username=? WHERE user_id=?", proposed.Email, key, "newsletter-"+hex.EncodeToString(nonce[:]), user.ID).Error; err != nil {
					return err
				}
				user.NewsletterMinimised = false
				sub.EmailKey = key
			}
			sub.Status = "confirmed"
			sub.Locale = proposed.Locale
			sub.ConsentVersion = proposed.ConsentVersion
			sub.Source = prefix + "blog/"
			sub.RequestedUnix = proposed.RequestedUnix
			sub.ConfirmedUnix = store.Ptr(now.Unix())
			sub.WithdrawnUnix = nil
			if err := tx.Model(&proof).Updates(map[string]any{"consumed_unix": now.Unix(), "proposal": ""}).Error; err != nil {
				return err
			}
			if err := mailqueue.InvalidateProtected(ctx, tx, now, "newsletter/"+hash); err != nil {
				return err
			}
		}
		sub.ProvedOrder = order
		if user.NewsletterMinimised {
			return tx.Model(&subscription{}).Where("user_id=?", user.ID).Updates(map[string]any{"status": sub.Status, "revision": sub.Revision, "proved_order": order, "withdrawn_unix": sub.WithdrawnUnix, "decision_unix": now.Unix()}).Error
		}
		return tx.Save(&sub).Error
	})
	if errors.Is(err, ErrProof) || errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrProof
	}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

// Run drains durable work without making the form wait for email delivery.
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		worked, err := s.Step(ctx)
		if err != nil {
			return err
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}
