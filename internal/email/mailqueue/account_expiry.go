package mailqueue

import (
	"context"
	"time"

	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

// SweepAccountProofSlice runs under exclusive account work ownership. It starts
// no signup/recovery worker and only removes ended, purpose-owned authority.
func SweepAccountProofSlice(ctx context.Context, tx *gorm.DB, now time.Time) (bool, error) {
	const batch = 100
	var before, after int64
	if err := tx.Raw("SELECT total_changes()").Row().Scan(&before); err != nil {
		return false, err
	}
	if err := tx.Exec("DELETE FROM signup_requests WHERE id IN (SELECT id FROM signup_requests WHERE expires<=? ORDER BY expires,id LIMIT ?)", now.Unix(), batch).Error; err != nil {
		return false, err
	}
	var signup []store.SignUpProof
	if err := tx.Where("expires<=? OR consumed<=?", now.Unix(), now.Add(-24*time.Hour).Unix()).Order("expires,id").Limit(batch).Find(&signup).Error; err != nil {
		return false, err
	}
	for _, proof := range signup {
		if err := InvalidateProtected(ctx, tx, now, "signup/"+proof.ID); err != nil {
			return false, err
		}
		if err := tx.Delete(&proof).Error; err != nil {
			return false, err
		}
	}
	var verification []store.EmailVerificationProof
	if err := tx.Where("expires<=? OR consumed<=?", now.Unix(), now.Add(-24*time.Hour).Unix()).Order("expires,id").Limit(batch).Find(&verification).Error; err != nil {
		return false, err
	}
	for _, proof := range verification {
		if err := InvalidateProtected(ctx, tx, now, "verify-email/"+proof.ID); err != nil {
			return false, err
		}
		if err := tx.Delete(&proof).Error; err != nil {
			return false, err
		}
	}
	var changes []store.PendingEmailChange
	if err := tx.Where("expires<=?", now.Unix()).Order("expires,id").Limit(batch).Find(&changes).Error; err != nil {
		return false, err
	}
	for _, change := range changes {
		for _, purpose := range []string{"current", "proposed"} {
			if err := InvalidateProtected(ctx, tx, now, "email-change/"+change.ID+"/"+purpose); err != nil {
				return false, err
			}
		}
		if err := tx.Delete(&change).Error; err != nil {
			return false, err
		}
	}
	if _, err := SweepRecoverySlice(ctx, tx, now); err != nil {
		return false, err
	}
	templates := []string{SignUpProofTemplate, EmailVerificationTemplate, EmailChangeCurrentTemplate, EmailChangeProposedTemplate, RecoveryProofTemplate, NewsletterENTemplate, NewsletterFRTemplate, NewsletterNLTemplate, NewsletterWithdrawalENTemplate, NewsletterWithdrawalFRTemplate, NewsletterWithdrawalNLTemplate}
	var mail []Record
	if err := tx.Where(`template IN ? AND (created_at<=? OR (state='failed' AND reason IN ('proof_invalidated','proof_expired')) OR (json_valid(data) AND ((json_extract(data,'$.expires_ns') IS NOT NULL AND json_extract(data,'$.expires_ns')<=?) OR (json_extract(data,'$.expires_ns') IS NULL AND json_extract(data,'$.expires')<=?))))`, templates, now.Add(-23*time.Hour).Unix(), now.UnixNano(), now.Unix()).Order("id").Limit(batch).Find(&mail).Error; err != nil {
		return false, err
	}
	for _, row := range mail {
		if row.ProviderID != nil {
			if err := tx.Exec("DELETE FROM email_events WHERE provider_id=?", *row.ProviderID).Error; err != nil {
				return false, err
			}
		}
		if err := tx.Delete(&row).Error; err != nil {
			return false, err
		}
	}
	if err := tx.Raw("SELECT total_changes()").Row().Scan(&after); err != nil {
		return false, err
	}
	changed := after > before
	if changed {
		if err := tx.Exec("UPDATE newsletter_identity_namespace SET physical_cleanup_pending=1 WHERE singleton=1").Error; err != nil {
			return false, err
		}
	}
	return changed, nil
}
