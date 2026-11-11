package auth

import (
	"context"
	"strconv"
	"time"

	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

// EraseAccountLinks joins deletion's transaction. Anonymous signup requests
// and another account's proposed-email request are not owned by this account.
func (m *Manager) EraseAccountLinks(ctx context.Context, tx *gorm.DB, user store.User, now time.Time) error {
	if len(m.RecoveryRateKey) != 32 {
		var eligible, pending int64
		if err := tx.Model(&store.RecoveryAccountOrder{}).Where("user_id=?", user.ID).Count(&eligible).Error; err != nil {
			return err
		}
		if err := tx.Model(&store.RecoveryJob{}).Count(&pending).Error; err != nil {
			return err
		}
		// The durable address index cannot be reconstructed without its original
		// key. Preserve all data rather than guess which anonymous jobs to remove.
		if eligible != 0 && pending != 0 {
			return store.ErrAccountDeletion
		}
	}
	var proofs []store.RecoveryProof
	if err := tx.Where("user_id=?", user.ID).Find(&proofs).Error; err != nil {
		return err
	}
	for _, proof := range proofs {
		if err := tx.Where("address_tag=? AND accepted_order<=?", proof.AddressTag, proof.AcceptedOrder).Delete(&store.RecoveryJob{}).Error; err != nil {
			return err
		}
		if len(m.RecoveryRateKey) == 32 {
			bucket, err := store.EmailRateBucket(m.RecoveryRateKey, "recovery-completion/proof", proof.Hash)
			if err != nil {
				return err
			}
			if err := tx.Exec("DELETE FROM email_rate_events WHERE bucket=?", bucket).Error; err != nil {
				return err
			}
		}
	}
	if err := invalidateRecovery(ctx, tx, now, user.ID, m.RecoveryRateKey, user.EmailKey); err != nil {
		return err
	}
	if err := consumeVerificationProofs(ctx, tx, now, user.ID); err != nil {
		return err
	}
	if err := cancelEmailChanges(ctx, tx, now, user.ID); err != nil {
		return err
	}
	if len(m.RecoveryRateKey) == 32 {
		for _, purpose := range []string{"email-change-password/user", "email-change-accepted/user"} {
			bucket, err := store.EmailRateBucket(m.RecoveryRateKey, purpose, strconv.FormatInt(user.ID, 10))
			if err != nil {
				return err
			}
			if err := tx.Exec("DELETE FROM email_rate_events WHERE bucket=?", bucket).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
