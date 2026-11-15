package store

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"gorm.io/gorm"
)

type newsletterTransactionKey struct{}
type newsletterTransactionState struct{ identities map[int64]bool }

// TrackNewsletterIdentity joins an existing account transaction. It records no
// mailbox and grants no authority; the caller must still verify its operation.
func TrackNewsletterIdentity(tx *gorm.DB, id int64) {
	if state, ok := tx.Statement.Context.Value(newsletterTransactionKey{}).(*newsletterTransactionState); ok {
		if _, exists := state.identities[id]; !exists {
			state.identities[id] = false
		}
	}
}

func (s *Store) newsletterTransactionContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, newsletterTransactionKey{}, &newsletterTransactionState{identities: make(map[int64]bool)})
}

// NewsletterTransaction commits the application changes and their journal fate
// markers together. The caller owns the account fence; this helper never nests
// it. A prepared intent survives every uncertain commit and blocks readiness.
func (s *Store) NewsletterTransaction(ctx context.Context, j *LifecycleJournal, apply func(*gorm.DB) error) error {
	if j != nil && j.app != s {
		return ErrNewsletterIdentityUnavailable
	}
	if j == nil {
		if s.newsletterJournalRequired.Load() {
			return ErrNewsletterIdentityUnavailable
		}
		return s.DB.WithContext(ctx).Transaction(apply)
	}
	_, bound, err := j.newsletterIdentityBinding(ctx)
	if err != nil {
		return err
	}
	if !bound {
		return s.DB.WithContext(ctx).Transaction(apply)
	}
	if err := j.CheckReady(ctx); err != nil {
		return err
	}
	scoped := s.newsletterTransactionContext(ctx)
	tx := s.DB.WithContext(scoped).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	var intents []NewsletterIdentityIntent
	if err = apply(tx); err == nil {
		intents, err = j.prepareNewsletterTransaction(scoped, tx, "")
	}
	if err != nil {
		if tx.Rollback().Error != nil {
			return ErrLifecycleUncertain
		}
		if err := j.cancelNewsletterTransaction(ctx, intents); err != nil {
			return err
		}
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return ErrLifecycleUncertain
	}
	return j.finishNewsletterTransaction(ctx, intents)
}

func (j *LifecycleJournal) cancelNewsletterTransaction(ctx context.Context, intents []NewsletterIdentityIntent) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, intent := range intents {
		if err := j.CancelNewsletterIdentity(cleanup, intent.Operation); err != nil {
			return err
		}
	}
	return nil
}
func (j *LifecycleJournal) finishNewsletterTransaction(ctx context.Context, intents []NewsletterIdentityIntent) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, intent := range intents {
		if err := j.CommitNewsletterIdentity(cleanup, intent.Operation); err != nil {
			return err
		}
	}
	return nil
}

// prepareNewsletterTransaction runs with the application transaction reserved.
// Every application read stays on tx; journal queries use its separate pool.
func (j *LifecycleJournal) prepareNewsletterTransaction(ctx context.Context, tx *gorm.DB, legacyOperation string) ([]NewsletterIdentityIntent, error) {
	state, ok := tx.Statement.Context.Value(newsletterTransactionKey{}).(*newsletterTransactionState)
	if !ok {
		return nil, nil
	}
	ids := make([]int64, 0, len(state.identities))
	for id := range state.identities {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	var records []NewsletterIdentityRecord
	for _, id := range ids {
		previous, found, err := readNewsletterIdentity(j.store.DB.WithContext(ctx), id)
		if err != nil {
			return nil, err
		}
		record, tracked, err := j.newsletterSQLIdentity(ctx, tx, id, previous, found, state.identities[id])
		if err != nil {
			return nil, err
		}
		if !tracked {
			continue
		}
		body, _, err := newsletterRecordPayload(record)
		if err != nil {
			return nil, err
		}
		if found {
			old, _, err := newsletterRecordPayload(previous)
			if err != nil {
				return nil, err
			}
			if old == body {
				continue
			}
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil, nil
	}
	var intents []NewsletterIdentityIntent
	err := j.store.DB.WithContext(ctx).Transaction(func(jtx *gorm.DB) error {
		var pending int64
		if err := jtx.Raw("SELECT (SELECT count(*) FROM newsletter_identity_intents)+(SELECT count(*) FROM lifecycle_deletions WHERE state='prepared' AND operation<>?)", legacyOperation).Scan(&pending).Error; err != nil || pending != 0 {
			return ErrLifecycleUncertain
		}
		for _, record := range records {
			current, found, err := readNewsletterIdentity(jtx, record.UserID)
			if err != nil || found && (current.Sequence != record.Sequence || current.Incarnation != record.Incarnation || current.Class == "app" && record.Class == "contact" || current.Class == "contact" && record.Class == "contact" && (current.KeyVersion != record.KeyVersion || current.AddressTag != record.AddressTag) || record.Class != "retired" && (record.Revision < current.Revision || record.DecisionOrder < current.DecisionOrder)) || !found && record.Sequence != 0 {
				return ErrLifecycleConflict
			}
			operation, err := randomLifecycleID()
			if err != nil {
				return err
			}
			body, hash, err := newsletterRecordPayload(record)
			if err != nil {
				return err
			}
			var sequence int64
			if err := jtx.Raw("UPDATE newsletter_identity_key SET next_sequence=next_sequence+1,high_water=MAX(high_water,?) WHERE singleton=1 AND next_sequence<? RETURNING next_sequence", record.UserID, int64(math.MaxInt64)).Row().Scan(&sequence); err != nil {
				return err
			}
			row := newsletterIdentityIntentRow{Operation: operation, Sequence: sequence, PreviousSequence: record.Sequence, RecordJSON: body, RecordHash: hash}
			if err := jtx.Create(&row).Error; err != nil {
				return err
			}
			intents = append(intents, NewsletterIdentityIntent{Operation: operation, Sequence: sequence, PreviousSequence: record.Sequence, Record: record, RecordHash: hash})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, intent := range intents {
		if err := j.MarkNewsletterIdentityCommit(ctx, tx, intent); err != nil {
			return intents, err
		}
	}
	return intents, nil
}

func (j *LifecycleJournal) newsletterSQLIdentity(ctx context.Context, tx *gorm.DB, id int64, old NewsletterIdentityRecord, known, created bool) (NewsletterIdentityRecord, bool, error) {
	var user User
	err := tx.WithContext(ctx).First(&user, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && known {
		return NewsletterIdentityRecord{UserID: id, Incarnation: old.Incarnation, Class: "retired", Sequence: old.Sequence}, true, nil
	}
	if err != nil {
		return old, false, err
	}
	if !known && !created {
		if user.IsNewsletterOnly {
			return old, false, ErrLifecycleUncertain
		}
		return old, false, nil // No newsletter origin is inferred for an app account.
	}
	record := NewsletterIdentityRecord{UserID: id, Class: "contact", Sequence: old.Sequence, Status: "pending", Minimised: user.NewsletterMinimised}
	if err := tx.Raw("SELECT incarnation FROM account_incarnations WHERE user_id=?", id).Row().Scan(&record.Incarnation); err != nil {
		return record, false, err
	}
	if known && record.Incarnation != old.Incarnation {
		return record, false, ErrLifecycleConflict
	}
	if user.IsNewsletterOnly {
		if known && old.Class == "app" {
			return record, false, ErrLifecycleConflict
		}
		if user.NewsletterMinimised {
			var match NewsletterIdentityMatch
			if err := tx.Take(&match, "user_id=?", id).Error; err != nil {
				return record, false, err
			}
			record.KeyVersion, record.AddressTag = match.KeyVersion, match.AddressTag
		} else {
			key := j.app.newsletterIdentity.Load()
			tag, err := key.Tag(user.Email)
			if err != nil {
				return record, false, err
			}
			record.KeyVersion, record.AddressTag = key.Version(), tag
		}
	} else {
		record.Class = "app"
		if user.NewsletterOwnershipEpoch == nil {
			if !known || old.Class != "contact" {
				return record, false, ErrLifecycleConflict
			}
			epoch, err := randomLifecycleID()
			if err != nil {
				return record, false, err
			}
			if err := tx.Model(&User{}).Where("user_id=?", id).Update("newsletter_ownership_epoch", epoch).Error; err != nil {
				return record, false, err
			}
			user.NewsletterOwnershipEpoch = Ptr(epoch)
		}
		record.OwnershipEpoch = *user.NewsletterOwnershipEpoch
		if err := tx.Exec("DELETE FROM newsletter_identity_matches WHERE user_id=?", id).Error; err != nil {
			return record, false, err
		}
		// A verified promotion restores only its now-required mailbox. Optional
		// newsletter locale/source/request history remain absent, never fabricated.
		if err := tx.Exec("UPDATE newsletter_subscriptions SET email_key=? WHERE user_id=? AND email_key IS NULL", user.EmailKey, id).Error; err != nil {
			return record, false, err
		}
	}
	var sub struct {
		Status                                                    string
		Revision, ProvedOrder                                     int64
		ConsentVersion                                            string
		RequestedUnix, ConfirmedUnix, WithdrawnUnix, DecisionUnix *int64
	}
	err = tx.Table("newsletter_subscriptions").Take(&sub, "user_id=?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return record, true, nil
	}
	if err != nil {
		return record, false, err
	}
	record.Status, record.Revision, record.DecisionOrder = sub.Status, sub.Revision, sub.ProvedOrder
	record.NoticeVersion = Ptr(sub.ConsentVersion)
	record.DecisionUnix = sub.RequestedUnix
	switch sub.Status {
	case "confirmed":
		record.DecisionUnix = sub.ConfirmedUnix
	case "unsubscribed":
		record.DecisionUnix = sub.WithdrawnUnix
	}
	if user.NewsletterMinimised || record.DecisionUnix == nil {
		record.DecisionUnix = sub.DecisionUnix
	}
	return record, true, nil
}

func (s *Store) reserveNewsletterCreate(tx *gorm.DB, user *User) error {
	if !s.newsletterJournalRequired.Load() {
		return nil
	}
	j := s.newsletterJournal.Load()
	if j == nil {
		return ErrNewsletterIdentityUnavailable
	}
	ctx := tx.Statement.Context
	if err := j.CheckReady(ctx); err != nil {
		return err
	}
	var floor int64
	if err := tx.Raw("SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name='users'),0)").Row().Scan(&floor); err != nil {
		return err
	}
	// New IDs must pass through this allocator. Explicit restored identities use
	// the separate stopped restore path, with their current journal authority.
	if user.ID != 0 {
		return ErrLifecycleConflict
	}
	if user.IsNewsletterOnly {
		if _, ok := ctx.Value(newsletterTransactionKey{}).(*newsletterTransactionState); !ok {
			return ErrNewsletterIdentityUnavailable
		}
	}
	if err := j.store.DB.WithContext(ctx).Raw("UPDATE newsletter_identity_key SET high_water=MAX(high_water,?)+1 WHERE singleton=1 AND high_water<? RETURNING high_water", floor, int64(math.MaxInt64)).Row().Scan(&user.ID); err != nil {
		return err
	}
	if state, ok := ctx.Value(newsletterTransactionKey{}).(*newsletterTransactionState); ok && user.IsNewsletterOnly {
		state.identities[user.ID] = true
	}
	return nil
}
