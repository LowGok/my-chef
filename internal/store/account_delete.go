package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"gorm.io/gorm"
)

var (
	ErrAccountConfirmation = errors.New("type DELETE to confirm account deletion")
	ErrAccountDeletion     = errors.New("account deletion unavailable")
)

type AccountDeletion struct{ Operation string }
type AccountDeletionOptions struct {
	// Hooks join the same database transaction. No provider request is made here.
	Cleanup     func(*gorm.DB, User) error
	Capture     func(*gorm.DB, User, string) error
	FinalNotice func(*gorm.DB, User, string) error
	Fault       func(string) error
}

func randomLifecycleID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", ErrAccountDeletion
	}
	return hex.EncodeToString(id[:]), nil
}
func deletionFault(opts AccountDeletionOptions, stage string) error {
	if opts.Fault != nil {
		return opts.Fault(stage)
	}
	return nil
}

func (j *LifecycleJournal) captureAsset(ctx context.Context, incarnation, kind, target, provider string) error {
	var existing LifecycleWork
	err := j.store.DB.WithContext(ctx).Where("kind=? AND target=? AND provider=?", kind, target, provider).Take(&existing).Error
	if err == nil {
		if existing.Incarnation != incarnation {
			return nil
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrLifecycleJournal
	}
	id, err := randomLifecycleID()
	if err != nil {
		return err
	}
	return j.reserveLifecycleWork(ctx, LifecycleWork{ID: id, Incarnation: incarnation, Kind: kind, Target: target, Provider: provider, State: "live", StartedNS: 1, Finished: true, Confirmed: true}, true)
}
func (j *LifecycleJournal) captureStoredAssets(ctx context.Context, tx *gorm.DB, user int64, incarnation string) error {
	var targets []struct {
		Target   string
		ImageURL *string
	}
	if err := tx.Raw(`SELECT DISTINCT cloudinary_public_id AS target,recipeimg AS image_url FROM meals m WHERE creator_id=? AND cloudinary_public_id IS NOT NULL AND cloudinary_public_id!=''`, user).Scan(&targets).Error; err != nil {
		return err
	}
	for _, target := range targets {
		if err := j.captureAsset(ctx, incarnation, "image", target.Target, assetURLBinding(target.ImageURL)); err != nil {
			return err
		}
	}
	return j.capturePersonalAssets(ctx, tx, user, incarnation)
}

func purgeAccountRows(tx *gorm.DB, user int64, incarnation string, opts AccountDeletionOptions) error {
	if err := tx.Exec("DELETE FROM personal_photo_limits WHERE key=?", "account:"+incarnation).Error; err != nil {
		return err
	}
	if err := tx.Exec("DELETE FROM signup_proofs WHERE id IN(SELECT proof_id FROM account_signup_owners WHERE incarnation=?)", incarnation).Error; err != nil {
		return err
	}
	// Feedback has deliberate non-cascading foreign keys. Link its notice before
	// removing the report, then remove only that account's attributable mail.
	if err := tx.Exec(`INSERT OR IGNORE INTO account_mail_owners SELECT r.outbox_id,? FROM feedback_reports r WHERE r.user_id=?`, incarnation, user).Error; err != nil {
		return err
	}
	for _, table := range []string{"feedback_reports", "feedback_ratings", "feedback_rating_receipts"} {
		if err := tx.Exec("DELETE FROM "+table+" WHERE user_id=?", user).Error; err != nil {
			return err
		}
	}
	if err := deletionFault(opts, "feedback"); err != nil {
		return err
	}
	if err := tx.Exec(`DELETE FROM email_events WHERE provider_id IN(SELECT provider_id FROM email_outbox WHERE id IN(SELECT outbox_id FROM account_mail_owners WHERE incarnation=?))`, incarnation).Error; err != nil {
		return err
	}
	if err := tx.Exec(`DELETE FROM email_outbox WHERE id IN(SELECT outbox_id FROM account_mail_owners WHERE incarnation=?)`, incarnation).Error; err != nil {
		return err
	}
	return deletionFault(opts, "mail")
}

// DeleteAccount serializes against email dispatch, commits the purge and its
// SQLite proof together, then durably commits the independent marker. An
// uncertain commit never returns success or cancels a potentially committed purge.
func (j *LifecycleJournal) DeleteAccount(ctx context.Context, actor AccountActor, operation, confirmation string, now time.Time, opts AccountDeletionOptions) (AccountDeletion, error) {
	if confirmation != "DELETE" || !opaqueLifecycleID(operation) {
		return AccountDeletion{}, ErrAccountConfirmation
	}
	j.app.AccountWork.Lock()
	defer j.app.AccountWork.Unlock()
	tx := j.app.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return AccountDeletion{}, ErrAccountDeletion
	}
	defer func() { _ = tx.Rollback().Error }()
	if err := accountAuthority(tx, actor, now); err != nil {
		return AccountDeletion{}, err
	}
	var user User
	if err := tx.First(&user, actor.UserID).Error; err != nil {
		return AccountDeletion{}, ErrAccountAccess
	}
	var incarnation string
	if err := tx.Raw("SELECT incarnation FROM account_incarnations WHERE user_id=?", user.ID).Row().Scan(&incarnation); err != nil {
		return AccountDeletion{}, ErrAccountDeletion
	}
	if err := j.captureStoredAssets(ctx, tx, user.ID, incarnation); err != nil {
		return AccountDeletion{}, ErrAccountDeletion
	}
	if opts.Capture != nil {
		if err := opts.Capture(tx, user, incarnation); err != nil {
			return AccountDeletion{}, ErrAccountDeletion
		}
	}
	if err := j.PrepareDeletion(ctx, operation, incarnation); err != nil {
		return AccountDeletion{}, err
	}
	// Cancellation must run after rollback and survive an HTTP client disconnect.
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback().Error
			repair, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = j.CancelDeletion(repair, operation)
		}
	}()
	if err := deletionFault(opts, "prepared"); err != nil {
		return AccountDeletion{}, ErrAccountDeletion
	}
	if opts.Cleanup != nil {
		if err := opts.Cleanup(tx, user); err != nil {
			return AccountDeletion{}, ErrAccountDeletion
		}
	}
	if err := purgeAccountRows(tx, user.ID, incarnation, opts); err != nil {
		return AccountDeletion{}, ErrAccountDeletion
	}
	notice := "skipped"
	if user.EmailVerifiedAt != nil && opts.FinalNotice != nil {
		if err := opts.FinalNotice(tx, user, operation); err != nil {
			return AccountDeletion{}, ErrAccountDeletion
		}
		notice = "queued"
	}
	if err := deletionFault(opts, "notice"); err != nil {
		return AccountDeletion{}, ErrAccountDeletion
	}
	result := tx.Where("user_id=?", user.ID).Delete(&User{})
	if result.Error != nil || result.RowsAffected != 1 {
		return AccountDeletion{}, ErrAccountDeletion
	}
	if err := deletionFault(opts, "user"); err != nil {
		return AccountDeletion{}, ErrAccountDeletion
	}
	if err := tx.Exec("INSERT INTO lifecycle_operations(operation,incarnation,created_ns,notice) VALUES(?,?,?,?)", operation, incarnation, now.UnixNano(), notice).Error; err != nil {
		return AccountDeletion{}, ErrAccountDeletion
	}
	if err := deletionFault(opts, "proof"); err != nil {
		return AccountDeletion{}, ErrAccountDeletion
	}
	if err := tx.Commit().Error; err != nil {
		return AccountDeletion{}, ErrLifecycleUncertain
	}
	committed = true
	j.CancelAccountWork(incarnation)
	if err := deletionFault(opts, "sqlite"); err != nil {
		return AccountDeletion{}, ErrLifecycleUncertain
	}
	repair, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := j.CommitDeletion(repair, operation); err != nil {
		return AccountDeletion{}, ErrLifecycleUncertain
	}
	if err := deletionFault(opts, "marker"); err != nil {
		return AccountDeletion{}, ErrLifecycleUncertain
	}
	return AccountDeletion{Operation: operation}, nil
}
