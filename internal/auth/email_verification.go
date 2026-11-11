package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

var ErrEmailProof = errors.New("invalid email proof")
var ErrEmailVerificationUnavailable = errors.New("email verification unavailable")

type EmailVerification struct {
	*SignUp
}

func boundEmailSession(db *gorm.DB, userID int64, token string, now time.Time) (store.User, error) {
	var user store.User
	if len(token) != 64 {
		return user, ErrSession
	}
	var session store.Session
	err := db.Where("hash = ? AND user_id = ? AND expires_unix > ?", Hash(token), userID, now.Unix()).Take(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return user, ErrSession
	}
	if err != nil {
		return user, err
	}
	err = db.First(&user, userID).Error
	return user, err
}

func newEmailProof() (token, hash string, err error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", "", ErrEmailVerificationUnavailable
	}
	token = base64.RawURLEncoding.EncodeToString(value[:])
	hash, err = signUpProofHash(token)
	return token, hash, err
}

func verificationOutboxKey(id string) string { return "verify-email/" + id }

func consumeVerificationProofs(ctx context.Context, tx *gorm.DB, now time.Time, userID int64) error {
	var proofs []store.EmailVerificationProof
	if err := tx.Where("user_id = ? AND consumed IS NULL", userID).Find(&proofs).Error; err != nil {
		return err
	}
	for _, proof := range proofs {
		if err := mailqueue.InvalidateProtected(ctx, tx, now, verificationOutboxKey(proof.ID)); err != nil {
			return err
		}
	}
	return tx.Model(&store.EmailVerificationProof{}).Where("user_id = ? AND consumed IS NULL", userID).
		Update("consumed", now.Unix()).Error
}

func pruneVerificationProofs(ctx context.Context, tx *gorm.DB, now time.Time) error {
	var proofs []store.EmailVerificationProof
	if err := tx.Where("expires <= ?", now.Unix()).Order("expires, id").Limit(32).Find(&proofs).Error; err != nil {
		return err
	}
	for _, proof := range proofs {
		if err := mailqueue.InvalidateProtected(ctx, tx, now, verificationOutboxKey(proof.ID)); err != nil {
			return err
		}
		if err := tx.Delete(&proof).Error; err != nil {
			return err
		}
	}
	return nil
}

func (v *EmailVerification) Resend(ctx context.Context, userID int64, sessionToken, source string) (int, error) {
	var wait int
	err := v.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := v.now()
		user, err := boundEmailSession(tx, userID, sessionToken, now)
		if err != nil || user.EmailVerifiedAt != nil {
			return err
		}
		addressBucket, err := store.EmailRateBucket(v.RateKey, "proof-delivery/address", user.EmailKey)
		if err != nil {
			return err
		}
		sourceBucket, err := store.EmailRateBucket(v.RateKey, "verify/source", source)
		if err != nil {
			return err
		}
		rate, err := v.Store.ReserveEmailLimits(ctx, tx, now,
			store.EmailLimit{Bucket: addressBucket, Maximum: 5, Window: 30 * time.Minute},
			store.EmailLimit{Bucket: sourceBucket, Maximum: 20, Window: 30 * time.Minute})
		if err != nil {
			return err
		}
		if wait = rate.RetryAfter; wait != 0 {
			return nil
		}
		token, hash, err := newEmailProof()
		if err != nil {
			return err
		}
		id, err := randomSignUpID()
		if err != nil {
			return err
		}
		tag, err := store.EmailRateBucket(v.RateKey, "verify-proof/address", user.Email)
		if err != nil {
			return err
		}
		link, err := v.proofURL("/app/verify-email", token)
		if err != nil {
			return err
		}
		if err := consumeVerificationProofs(ctx, tx, now, user.ID); err != nil {
			return err
		}
		expiry := now.Add(24 * time.Hour)
		proof := store.EmailVerificationProof{ID: id, UserID: user.ID, Hash: hash, AddressTag: tag, Expires: expiry.Unix()}
		if err := tx.Create(&proof).Error; err != nil {
			return err
		}
		_, err = mailqueue.EnqueueProtected(ctx, v.Store, tx, now, expiry, v.Keys, mailqueue.Request{
			OwnerID: user.ID, Key: verificationOutboxKey(id), Recipient: user.Email, Template: mailqueue.EmailVerificationTemplate,
			Fields: map[string]string{"link": link},
		})
		return err
	})
	if err != nil && !errors.Is(err, ErrSession) {
		return 0, ErrEmailVerificationUnavailable
	}
	return wait, err
}

// Confirm rechecks the session, proof and exact delivery address in the
// transaction that grants private access and replaces the restricted session.
func (v *EmailVerification) Confirm(ctx context.Context, userID int64, sessionToken, token string) (store.User, string, error) {
	hash, err := signUpProofHash(token)
	if err != nil {
		return store.User{}, "", ErrEmailProof
	}
	var user store.User
	var replacement string
	err = v.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := v.now()
		current, err := boundEmailSession(tx, userID, sessionToken, now)
		if err != nil {
			return err
		}
		if current.EmailVerifiedAt != nil {
			return ErrEmailProof
		}
		var proof store.EmailVerificationProof
		err = tx.Where("hash = ? AND user_id = ? AND consumed IS NULL AND expires > ?", hash, userID, now.Unix()).Take(&proof).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEmailProof
		}
		if err != nil {
			return err
		}
		tag, err := store.EmailRateBucket(v.RateKey, "verify-proof/address", current.Email)
		if err != nil {
			return err
		}
		if proof.AddressTag != tag {
			return ErrEmailProof
		}
		verified := now.Unix()
		if err := tx.Model(&current).Update("email_verified_at", verified).Error; err != nil {
			return err
		}
		if err := invalidateRecovery(ctx, tx, now, userID, v.RateKey, current.EmailKey); err != nil {
			return err
		}
		if err := consumeVerificationProofs(ctx, tx, now, userID); err != nil {
			return err
		}
		if err := cancelEmailChanges(ctx, tx, now, userID); err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&store.Session{}).Error; err != nil {
			return err
		}
		value, session, err := newSession(&userID, now)
		if err != nil {
			return err
		}
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		current.EmailVerifiedAt = &verified
		user, replacement = current, value
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrEmailProof) || errors.Is(err, ErrSession) {
			return store.User{}, "", err
		}
		return store.User{}, "", ErrEmailVerificationUnavailable
	}
	return user, replacement, nil
}
