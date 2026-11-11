package auth

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

const emailChangeWindow = 24 * time.Hour

var ErrEmailChangeUnavailable = errors.New("email change unavailable")

type EmailChange struct {
	*SignUp
}

type EmailChangeView struct {
	Action        string `json:"action"`
	MaskedAddress string `json:"maskedAddress,omitempty"`
}

type EmailChangeResult struct {
	Status string
	UserID int64
}

func (c *EmailChange) Start(ctx context.Context, userID int64, sessionToken, source, proposed, password string) (int, error) {
	display, key, err := store.EmailIdentity(proposed)
	if err != nil {
		return 0, ErrInput
	}
	user, err := boundEmailSession(c.Store.DB.WithContext(ctx), userID, sessionToken, c.now())
	if err != nil {
		return 0, changeError(err)
	}
	if key == user.EmailKey {
		return 0, ErrInput
	}
	sourceBucket, err := store.EmailRateBucket(c.RateKey, "email-change-start/source", source)
	if err != nil {
		return 0, ErrEmailChangeUnavailable
	}
	passwordBucket, err := store.EmailRateBucket(c.RateKey, "email-change-password/user", strconv.FormatInt(userID, 10))
	if err != nil {
		return 0, ErrEmailChangeUnavailable
	}
	acceptedLimit, err := c.acceptedStartLimit(userID)
	if err != nil {
		return 0, err
	}
	var attempt store.EmailLimitResult
	err = c.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := c.now()
		var err error
		attempt, err = c.Store.ReserveEmailLimits(ctx, tx, now,
			store.EmailLimit{Bucket: sourceBucket, Maximum: 20, Window: 30 * time.Minute},
			store.EmailLimit{Bucket: passwordBucket, Maximum: 5, Window: 15 * time.Minute})
		if err != nil || attempt.RetryAfter == 0 {
			return err
		}
		// A blocked attempt cannot test a password or inspect the proposed
		// inbox. Its own account's daily limits can still outlast this wait.
		current, err := boundEmailSession(tx, userID, sessionToken, now)
		if err != nil {
			return err
		}
		acceptedWait, err := c.Store.EmailLimitsRetryAfter(ctx, tx, now, acceptedLimit)
		if err != nil {
			return err
		}
		attempt.RetryAfter = max(attempt.RetryAfter, acceptedWait, cooldownSeconds(now, current.EmailChangedAt))
		return nil
	})
	if err != nil || attempt.RetryAfter != 0 {
		return attempt.RetryAfter, changeError(err)
	}
	normalized, normalizeErr := normalizePassword(password)
	if normalizeErr != nil {
		return 0, ErrCurrentPassword
	}
	match, _, err := comparePassword(ctx, user.Password, password, normalized)
	if err == nil && !match {
		return 0, ErrCurrentPassword
	}
	if releaseErr := c.Store.ReleaseEmailLimit(ctx, nil, attempt.Reservation, passwordBucket); releaseErr != nil || err != nil {
		return 0, ErrEmailChangeUnavailable
	}
	return c.commitStart(ctx, user, sessionToken, display, key)
}

func (c *EmailChange) acceptedStartLimit(userID int64) (store.EmailLimit, error) {
	bucket, err := store.EmailRateBucket(c.RateKey, "email-change-accepted/user", strconv.FormatInt(userID, 10))
	if err != nil {
		return store.EmailLimit{}, ErrEmailChangeUnavailable
	}
	return store.EmailLimit{Bucket: bucket, Maximum: 2, Window: emailChangeWindow}, nil
}

func (c *EmailChange) commitStart(ctx context.Context, snapshot store.User, sessionToken, display, key string) (int, error) {
	var wait int
	err := c.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := c.now()
		user, err := boundEmailSession(tx, snapshot.ID, sessionToken, now)
		if err != nil {
			return err
		}
		if user.Password != snapshot.Password || !sameAddressState(user, snapshot.Email, snapshot.EmailKey, snapshot.EmailVerifiedAt) {
			return ErrSession
		}
		acceptedLimit, err := c.acceptedStartLimit(user.ID)
		if err != nil {
			return err
		}
		emailBucket, err := store.EmailRateBucket(c.RateKey, "proof-delivery/address", key)
		if err != nil {
			return err
		}
		deliveryLimit := store.EmailLimit{Bucket: emailBucket, Maximum: 5, Window: 30 * time.Minute}
		if cooldown := cooldownSeconds(now, user.EmailChangedAt); cooldown != 0 {
			retry, err := c.Store.EmailLimitsRetryAfter(ctx, tx, now, acceptedLimit, deliveryLimit)
			wait = max(cooldown, retry)
			return err
		}
		rate, err := c.Store.ReserveEmailLimits(ctx, tx, now, acceptedLimit, deliveryLimit)
		if err != nil {
			return err
		}
		if wait = rate.RetryAfter; wait != 0 {
			return nil
		}
		if err := cancelEmailChanges(ctx, tx, now, user.ID); err != nil {
			return err
		}
		id, err := randomSignUpID()
		if err != nil {
			return err
		}
		pending := store.PendingEmailChange{ID: id, UserID: user.ID, OriginalEmail: user.Email, OriginalKey: user.EmailKey,
			OriginalVerifiedAt: user.EmailVerifiedAt, ProposedEmail: display, ProposedKey: key, Expires: now.Add(emailChangeWindow).Unix()}
		if err := tx.Create(&pending).Error; err != nil {
			return err
		}
		return c.issueChangeProofs(ctx, tx, now, pending)
	})
	return wait, changeError(err)
}

func (c *EmailChange) issueChangeProofs(ctx context.Context, tx *gorm.DB, now time.Time, pending store.PendingEmailChange) error {
	makeProof := func(purpose string) (string, error) {
		token, hash, err := newEmailProof()
		if err != nil {
			return "", err
		}
		link, err := c.proofURL("/app/change-email", token)
		if err != nil {
			return "", err
		}
		return link, tx.Create(&store.EmailChangeProof{Hash: hash, ChangeID: pending.ID, Purpose: purpose}).Error
	}
	queue := func(suffix, to, template string, fields map[string]string) error {
		_, err := mailqueue.EnqueueProtected(ctx, c.Store, tx, now, time.Unix(pending.Expires, 0), c.Keys, mailqueue.Request{
			OwnerID: pending.UserID, Key: changeMailKey(pending.ID, suffix), Recipient: to, Template: template, Fields: fields,
		})
		return err
	}
	if pending.OriginalVerifiedAt != nil {
		approve, err := makeProof("approve")
		if err != nil {
			return err
		}
		cancel, err := makeProof("cancel")
		if err != nil {
			return err
		}
		if err := queue("current", pending.OriginalEmail, mailqueue.EmailChangeCurrentTemplate,
			map[string]string{"approve": approve, "cancel": cancel}); err != nil {
			return err
		}
	}
	confirm, err := makeProof("confirm")
	if err != nil {
		return err
	}
	return queue("proposed", pending.ProposedEmail, mailqueue.EmailChangeProposedTemplate, map[string]string{"link": confirm})
}

func changeMailKey(id, purpose string) string { return "email-change/" + id + "/" + purpose }

func removeEmailChange(ctx context.Context, tx *gorm.DB, now time.Time, pending store.PendingEmailChange) error {
	for _, purpose := range []string{"current", "proposed"} {
		if err := mailqueue.InvalidateProtected(ctx, tx, now, changeMailKey(pending.ID, purpose)); err != nil {
			return err
		}
	}
	return tx.Delete(&pending).Error
}

func cancelEmailChanges(ctx context.Context, tx *gorm.DB, now time.Time, userID int64) error {
	var pending store.PendingEmailChange
	err := tx.Where("user_id = ?", userID).Take(&pending).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return removeEmailChange(ctx, tx, now, pending)
}

func pruneEmailChanges(ctx context.Context, tx *gorm.DB, now time.Time) error {
	var pending []store.PendingEmailChange
	if err := tx.Where("expires <= ?", now.Unix()).Order("expires, id").Limit(32).Find(&pending).Error; err != nil {
		return err
	}
	for _, change := range pending {
		if err := removeEmailChange(ctx, tx, now, change); err != nil {
			return err
		}
	}
	return nil
}

func cooldownSeconds(now time.Time, changed *int64) int {
	if changed == nil {
		return 0
	}
	return max(0, int(*changed+int64(emailChangeWindow/time.Second)-now.Unix()))
}

func sameAddressState(user store.User, email, key string, verified *int64) bool {
	if user.Email != email || user.EmailKey != key || (user.EmailVerifiedAt == nil) != (verified == nil) {
		return false
	}
	return verified == nil || *user.EmailVerifiedAt == *verified
}

func maskedEmail(value string) string {
	at := strings.LastIndexByte(value, '@')
	if at <= 0 {
		return ""
	}
	_, width := utf8.DecodeRuneInString(value[:at])
	return value[:width] + "•••" + value[at:]
}

func loadEmailChange(tx *gorm.DB, hash string, now time.Time) (store.EmailChangeProof, store.PendingEmailChange, store.User, error) {
	var proof store.EmailChangeProof
	var pending store.PendingEmailChange
	var user store.User
	err := tx.Where("hash = ? AND consumed IS NULL", hash).Take(&proof).Error
	if err == nil {
		err = tx.Where("id = ? AND expires > ?", proof.ChangeID, now.Unix()).Take(&pending).Error
	}
	if err == nil {
		err = tx.First(&user, pending.UserID).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrEmailProof
	}
	if err == nil && !sameAddressState(user, pending.OriginalEmail, pending.OriginalKey, pending.OriginalVerifiedAt) {
		err = ErrEmailProof
	}
	return proof, pending, user, err
}

func (c *EmailChange) Inspect(ctx context.Context, token string) (EmailChangeView, error) {
	hash, err := signUpProofHash(token)
	if err != nil {
		return EmailChangeView{}, ErrEmailProof
	}
	var view EmailChangeView
	err = c.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		proof, pending, _, err := loadEmailChange(tx, hash, c.now())
		if err != nil {
			return err
		}
		view.Action = proof.Purpose
		if proof.Purpose != "confirm" {
			view.MaskedAddress = maskedEmail(pending.ProposedEmail)
		}
		return nil
	})
	return view, changeError(err)
}

func (c *EmailChange) Decide(ctx context.Context, token string) (EmailChangeResult, error) {
	hash, err := signUpProofHash(token)
	if err != nil {
		return EmailChangeResult{}, ErrEmailProof
	}
	var result EmailChangeResult
	var unavailable bool
	err = c.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := c.now()
		proof, pending, user, err := loadEmailChange(tx, hash, now)
		if err != nil {
			return err
		}
		result.UserID = user.ID
		if proof.Purpose == "cancel" {
			result.Status = "cancelled"
			return removeEmailChange(ctx, tx, now, pending)
		}
		switch proof.Purpose {
		case "approve":
			pending.CurrentApproved = true
		case "confirm":
			pending.ProposedApproved = true
		default:
			return ErrEmailProof
		}
		if pending.ProposedApproved && (pending.OriginalVerifiedAt == nil || pending.CurrentApproved) {
			claimed, err := addressClaimed(tx, pending.ProposedKey)
			if err != nil {
				return err
			}
			if claimed || cooldownSeconds(now, user.EmailChangedAt) != 0 {
				unavailable = true
				return removeEmailChange(ctx, tx, now, pending)
			}
			result.Status = "changed"
			return c.finishChange(ctx, tx, now, user, pending)
		}
		if err := tx.Model(&proof).Update("consumed", now.Unix()).Error; err != nil {
			return err
		}
		result.Status = "pending"
		return tx.Model(&pending).Updates(map[string]any{"current_approved": pending.CurrentApproved,
			"proposed_approved": pending.ProposedApproved}).Error
	})
	if err != nil {
		return EmailChangeResult{}, changeError(err)
	}
	if unavailable {
		return EmailChangeResult{}, ErrEmailProof
	}
	return result, nil
}

func (c *EmailChange) finishChange(ctx context.Context, tx *gorm.DB, now time.Time, user store.User, pending store.PendingEmailChange) error {
	if err := tx.Model(&user).Updates(map[string]any{
		"email": pending.ProposedEmail, "email_key": pending.ProposedKey,
		"email_verified_at": now.Unix(), "email_changed_at": now.Unix(),
	}).Error; err != nil {
		return err
	}
	if err := invalidateRecovery(ctx, tx, now, user.ID, c.RateKey, pending.OriginalKey, pending.ProposedKey); err != nil {
		return err
	}
	if err := consumeVerificationProofs(ctx, tx, now, user.ID); err != nil {
		return err
	}
	tag, err := store.EmailRateBucket(c.RateKey, "signup-proof/address", pending.ProposedKey)
	if err != nil {
		return err
	}
	if err := consumeSignUpAddress(ctx, tx, now, tag); err != nil {
		return err
	}
	if err := tx.Where("user_id = ?", user.ID).Delete(&store.Session{}).Error; err != nil {
		return err
	}
	if err := removeEmailChange(ctx, tx, now, pending); err != nil {
		return err
	}
	notice := func(purpose, to string) error {
		_, err := mailqueue.Enqueue(ctx, c.Store, tx, now, mailqueue.Request{OwnerID: user.ID, Key: changeMailKey(pending.ID, purpose),
			Recipient: to, Template: mailqueue.EmailChangedTemplate, Fields: map[string]string{"timestamp": now.Format(time.RFC3339)}})
		return err
	}
	if pending.OriginalVerifiedAt != nil {
		if err := notice("done-current", pending.OriginalEmail); err != nil {
			return err
		}
	}
	return notice("done-proposed", pending.ProposedEmail)
}

func changeError(err error) error {
	if err == nil || errors.Is(err, ErrSession) || errors.Is(err, ErrEmailProof) || errors.Is(err, ErrCurrentPassword) || errors.Is(err, ErrInput) {
		return err
	}
	return ErrEmailChangeUnavailable
}
