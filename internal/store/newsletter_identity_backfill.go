package store

import (
	"context"
)

// BackfillNewsletterIdentities is an explicit stopped migration. Once the
// registry is complete it validates existing rows and refuses late adoption.
func (j *LifecycleJournal) BackfillNewsletterIdentities(ctx context.Context, key *NewsletterIdentityKey) error {
	if err := j.InitializeNewsletterIdentity(ctx, key); err != nil {
		return err
	}
	j.app.AccountWork.Lock()
	err := j.backfillNewsletterIdentitiesOwned(ctx)
	j.app.AccountWork.Unlock()
	if err != nil {
		return err
	}
	return j.CompleteNewsletterIdentityBootstrap(ctx)
}
func (j *LifecycleJournal) backfillNewsletterIdentitiesOwned(ctx context.Context) error {
	var binding newsletterIdentityJournalKey
	if err := j.store.DB.WithContext(ctx).Take(&binding, "singleton=1").Error; err != nil {
		return err
	}
	after := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var users []User
		if err := j.app.DB.WithContext(ctx).Where("isNewsletterOnly=1 AND user_id>?", after).Order("user_id").Limit(100).Find(&users).Error; err != nil {
			return err
		}
		if len(users) == 0 {
			return nil
		}
		for _, user := range users {
			after = user.ID
			record, found, err := readNewsletterIdentity(j.store.DB.WithContext(ctx), user.ID)
			if err != nil {
				return err
			}
			if found {
				if err := j.validateNewsletterIdentityPostState(ctx, j.app.DB, record); err != nil {
					return err
				}
				continue
			}
			if binding.Complete {
				return ErrLifecycleUncertain
			}
			scoped := j.app.newsletterTransactionContext(ctx)
			tx := j.app.DB.WithContext(scoped).Begin()
			if tx.Error != nil {
				return tx.Error
			}
			state := scoped.Value(newsletterTransactionKey{}).(*newsletterTransactionState)
			state.identities[user.ID] = true
			intents, err := j.prepareNewsletterTransaction(scoped, tx, "")
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
			if err := j.finishNewsletterTransaction(ctx, intents); err != nil {
				return err
			}
		}
	}
}
