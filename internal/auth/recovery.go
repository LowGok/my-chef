package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

const recoveryLifetime = 30 * time.Minute
const recoveryLinkPath = "/app/reset-password"

type Recovery struct {
	*SignUp
	Work *store.RecoveryWork
}

func recoveryJobBinding(job store.RecoveryJob) string {
	return fmt.Sprintf("%d:%d:%s", job.AcceptedOrder, job.ExpiresNS, job.AddressTag)
}

func (r *Recovery) recoveryAddressTag(key string) (string, error) {
	return store.EmailRateBucket(r.RateKey, "recovery-job/address", key)
}

// Request records a durable encrypted job without reading an account.
func (r *Recovery) Request(ctx context.Context, source, address string) (int, error) {
	_, key, err := store.EmailIdentity(address)
	if err != nil {
		return 0, ErrInput
	}
	if r == nil || r.SignUp == nil || r.Keys == nil {
		return 0, store.ErrRecoveryUnavailable
	}
	if _, err := r.proofURL(recoveryLinkPath, ""); err != nil {
		return 0, store.ErrRecoveryUnavailable
	}
	sourceBucket, err := store.EmailRateBucket(r.RateKey, "recovery-request/source", source)
	if err != nil {
		return 0, store.ErrRecoveryUnavailable
	}
	emailBucket, err := store.EmailRateBucket(r.RateKey, "recovery-request/address", key)
	if err != nil {
		return 0, store.ErrRecoveryUnavailable
	}
	tag, err := r.recoveryAddressTag(key)
	if err != nil {
		return 0, store.ErrRecoveryUnavailable
	}
	var wait int
	err = r.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := r.now()
		result, err := r.Store.ReserveEmailLimits(ctx, tx, now,
			store.EmailLimit{Bucket: sourceBucket, Maximum: 20, Window: recoveryLifetime},
			store.EmailLimit{Bucket: emailBucket, Maximum: 5, Window: recoveryLifetime})
		if err != nil {
			return err
		}
		if wait = result.RetryAfter; wait != 0 {
			return nil
		}
		order, err := store.NextRecoveryOrder(ctx, tx)
		if err != nil {
			return err
		}
		job := store.RecoveryJob{AcceptedOrder: order, AddressTag: tag, ExpiresNS: now.Add(recoveryLifetime).UnixNano()}
		box, err := r.Keys.Seal("recovery-job", recoveryJobBinding(job), []byte(key))
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(box)
		if err != nil {
			return err
		}
		job.Draft = string(encoded)
		return tx.Create(&job).Error
	})
	if err != nil {
		return 0, store.ErrRecoveryUnavailable
	}
	return wait, nil
}

func (r *Recovery) openRecoveryJob(job store.RecoveryJob) (string, bool) {
	var box mailqueue.SealedProof
	if json.Unmarshal([]byte(job.Draft), &box) != nil {
		return "", false
	}
	raw, err := r.Keys.Open("recovery-job", recoveryJobBinding(job), box)
	if err != nil {
		return "", false
	}
	key := string(raw)
	tag, err := r.recoveryAddressTag(key)
	return key, err == nil && tag == job.AddressTag
}

func consumeRecoveryProofs(ctx context.Context, tx *gorm.DB, now time.Time, userID int64) error {
	var older []store.RecoveryProof
	if err := tx.Where("user_id = ? AND consumed_ns IS NULL", userID).Find(&older).Error; err != nil {
		return err
	}
	for _, proof := range older {
		if err := mailqueue.InvalidateProtected(ctx, tx, now, mailqueue.RecoveryMailKey(proof.AcceptedOrder)); err != nil {
			return err
		}
	}
	return tx.Model(&store.RecoveryProof{}).Where("user_id = ? AND consumed_ns IS NULL", userID).
		Update("consumed_ns", now.UnixNano()).Error
}

func (r *Recovery) issueRecoveryProof(ctx context.Context, tx *gorm.DB, now time.Time, job store.RecoveryJob,
	user store.User, orders store.RecoveryAccountOrder) error {
	token, hash, err := newEmailProof()
	if err != nil {
		return err
	}
	link, err := r.proofURL(recoveryLinkPath, token)
	if err != nil {
		return err
	}
	if err := consumeRecoveryProofs(ctx, tx, now, user.ID); err != nil {
		return err
	}
	expires := now.Add(recoveryLifetime)
	proof := store.RecoveryProof{Hash: hash, AcceptedOrder: job.AcceptedOrder, UserID: user.ID,
		AddressTag: job.AddressTag, EligibilityOrder: orders.EligibilityOrder,
		IssuedNS: now.UnixNano(), ExpiresNS: expires.UnixNano()}
	if err := tx.Create(&proof).Error; err != nil {
		return err
	}
	if _, err := mailqueue.EnqueueProtected(ctx, r.Store, tx, now, expires, r.Keys, mailqueue.Request{
		OwnerID: user.ID, Key: mailqueue.RecoveryMailKey(job.AcceptedOrder), Recipient: user.Email, Template: mailqueue.RecoveryProofTemplate,
		Fields: map[string]string{"link": link},
	}); err != nil {
		return err
	}
	return tx.Model(&orders).Update("latest_issued_order", job.AcceptedOrder).Error
}

// Step resolves at most one job. Account and proof work is separate from intake.
func (r *Recovery) Step(ctx context.Context) (bool, error) {
	return r.step(ctx, 0)
}

func (r *Recovery) step(ctx context.Context, order int64) (bool, error) {
	var worked bool
	err := r.Work.Background(ctx, func() error {
		return r.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			now := r.now()
			var job store.RecoveryJob
			jobs := tx.Order("accepted_order")
			if order != 0 {
				jobs = jobs.Where("accepted_order = ?", order)
			}
			err := jobs.Take(&job).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			worked = true
			if job.ExpiresNS <= now.UnixNano() {
				return tx.Delete(&job).Error
			}
			key, ok := r.openRecoveryJob(job)
			if !ok {
				return tx.Delete(&job).Error
			}
			var user store.User
			err = tx.Where("email_key = ? AND email_verified_at IS NOT NULL", key).Take(&user).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return tx.Delete(&job).Error
			}
			if err != nil {
				return err
			}
			var orders store.RecoveryAccountOrder
			err = tx.Where("user_id = ?", user.ID).Take(&orders).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return tx.Delete(&job).Error // No trusted ordering for this address.
			}
			if err != nil {
				return err
			}
			if job.AcceptedOrder > orders.EligibilityOrder && job.AcceptedOrder > orders.LatestIssuedOrder {
				if err := r.issueRecoveryProof(ctx, tx, now, job, user, orders); err != nil {
					return err
				}
			}
			return tx.Delete(&job).Error
		})
	})
	if err != nil {
		return false, store.ErrRecoveryUnavailable
	}
	return worked, nil
}

func (r *Recovery) Run(ctx context.Context) error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for ctx.Err() == nil {
		worked, err := r.Step(ctx)
		if err != nil {
			return err
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
	return nil
}

// Sweep is independent of provider sends and yields between bounded slices.
func (r *Recovery) Sweep(ctx context.Context) error {
	for {
		var more bool
		err := r.Work.Background(ctx, func() error {
			return r.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var err error
				more, err = mailqueue.SweepRecoverySlice(ctx, tx, r.now())
				return err
			})
		})
		if err != nil {
			return store.ErrRecoveryUnavailable
		}
		if !more {
			return nil
		}
	}
}

func (r *Recovery) RunSweep(ctx context.Context) error {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if err := r.Sweep(ctx); err != nil {
				return err
			}
		}
	}
}

var ErrRecoveryProof = errors.New("invalid password recovery link")

// RecoveryCompletionBucket is the same bucket used by HTTP reservation and
// the successful credential transaction. Malformed bounded proofs also hash.
func (r *Recovery) RecoveryCompletionBucket(proof string) (string, error) {
	return store.EmailRateBucket(r.RateKey, "recovery-completion/proof", Hash(proof))
}

func requireAnonymousRecovery(tx *gorm.DB, cookie string, now time.Time) error {
	if len(cookie) != 64 {
		return ErrSession
	}
	var session store.Session
	err := tx.Where("hash = ? AND user_id IS NULL AND expires_unix > ?", Hash(cookie), now.Unix()).Take(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrSession
	}
	return err
}

func (r *Recovery) readRecoveryProof(tx *gorm.DB, hash string, now time.Time) (store.RecoveryProof, store.User, error) {
	var proof store.RecoveryProof
	var user store.User
	if err := tx.Where("hash = ? AND consumed_ns IS NULL AND expires_ns > ?", hash, now.UnixNano()).Take(&proof).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = ErrRecoveryProof
		}
		return proof, user, err
	}
	if err := tx.Where("user_id = ? AND email_verified_at IS NOT NULL", proof.UserID).Take(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = ErrRecoveryProof
		}
		return proof, user, err
	}
	var orders store.RecoveryAccountOrder
	if err := tx.Where("user_id = ?", user.ID).Take(&orders).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = ErrRecoveryProof
		}
		return proof, user, err
	}
	tag, err := r.recoveryAddressTag(user.EmailKey)
	if err != nil {
		return proof, user, err
	}
	if proof.AddressTag != tag || orders.EligibilityOrder != proof.EligibilityOrder || orders.LatestIssuedOrder != proof.AcceptedOrder {
		return proof, user, ErrRecoveryProof
	}
	return proof, user, nil
}

func recoveryError(err error) error {
	if err == nil || errors.Is(err, ErrRecoveryProof) || errors.Is(err, ErrSession) ||
		errors.Is(err, ErrPasswordLength) || errors.Is(err, ErrPasswordCommon) || errors.Is(err, ErrInput) {
		return err
	}
	return store.ErrRecoveryUnavailable
}

func (r *Recovery) prepareRecovery(ctx context.Context, anonymous, proof, password string) (string, error) {
	db := r.Store.DB.WithContext(ctx)
	now := r.now()
	if err := requireAnonymousRecovery(db, anonymous, now); err != nil {
		return "", recoveryError(err)
	}
	hash, err := signUpProofHash(proof)
	if err != nil {
		return "", ErrRecoveryProof
	}
	if _, _, err := r.readRecoveryProof(db, hash, now); err != nil {
		return "", recoveryError(err)
	}
	normalized, err := checkNewPassword(password)
	if err != nil {
		return "", recoveryError(err)
	}
	credential, err := newPasswordHash(ctx, normalized)
	return credential, recoveryError(err)
}

func (r *Recovery) Complete(ctx context.Context, anonymous, proof, password string) error {
	if r == nil || r.SignUp == nil || r.Keys == nil {
		return store.ErrRecoveryUnavailable
	}
	credential, err := r.prepareRecovery(ctx, anonymous, proof, password)
	if err != nil {
		return err
	}
	return r.commitRecovery(ctx, anonymous, proof, credential)
}

func (r *Recovery) commitRecovery(ctx context.Context, anonymous, token, credential string) error {
	hash, err := signUpProofHash(token)
	if err != nil {
		return ErrRecoveryProof
	}
	bucket, err := r.RecoveryCompletionBucket(token)
	if err != nil {
		return store.ErrRecoveryUnavailable
	}
	err = r.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := r.now()
		if err := requireAnonymousRecovery(tx, anonymous, now); err != nil {
			return err
		}
		proof, user, err := r.readRecoveryProof(tx, hash, now)
		if err != nil {
			return err
		}
		if err := tx.Model(&user).Update("password", credential).Error; err != nil {
			return err
		}
		if err := invalidateRecovery(ctx, tx, now, user.ID, r.RateKey, user.EmailKey); err != nil {
			return err
		}
		if err := cancelEmailChanges(ctx, tx, now, user.ID); err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&store.Session{}).Error; err != nil {
			return err
		}
		if _, err := mailqueue.Enqueue(ctx, r.Store, tx, now, mailqueue.Request{
			OwnerID: user.ID, Key: fmt.Sprintf("password-reset/%d", proof.AcceptedOrder), Recipient: user.Email,
			Template: mailqueue.PasswordChangedTemplate, Fields: map[string]string{"timestamp": now.Format(time.RFC3339)},
		}); err != nil {
			return err
		}
		return r.Store.ClearEmailLimit(ctx, tx, bucket)
	})
	return recoveryError(err)
}

func invalidateRecovery(ctx context.Context, tx *gorm.DB, now time.Time, userID int64, rateKey []byte, keys ...string) error {
	order, err := store.AdvanceRecoveryFence(ctx, tx, userID)
	if err != nil {
		return err
	}
	if err := consumeRecoveryProofs(ctx, tx, now, userID); err != nil {
		return err
	}
	// The durable fence invalidates old jobs even if the external key is
	// absent. Without that key their address index cannot be derived; the
	// independent expiry sweep still erases their ciphertext.
	if len(rateKey) == 0 {
		return nil
	}
	for _, key := range keys {
		tag, err := store.EmailRateBucket(rateKey, "recovery-job/address", key)
		if err != nil {
			return err
		}
		if err := tx.Where("address_tag = ? AND accepted_order < ?", tag, order).Delete(&store.RecoveryJob{}).Error; err != nil {
			return err
		}
	}
	return nil
}
