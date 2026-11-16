package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// newsletterRestoreBinding authenticates the current independent journal before
// using a snapshot. Missing SQL metadata may be old; a missing current journal
// binding, changed key or newer SQL checkpoint is never silently adopted.
func (j *LifecycleJournal) newsletterRestoreBinding(ctx context.Context, key *NewsletterIdentityKey) (bool, error) {
	var current newsletterIdentityJournalKey
	err := j.store.DB.WithContext(ctx).Take(&current, "singleton=1").Error
	var exists int
	if e := j.app.SQL.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name='newsletter_identity_namespace'").Scan(&exists); e != nil {
		return false, e
	}
	var saved NewsletterIdentityNamespace
	savedFound := false
	if exists != 0 {
		e := j.app.DB.WithContext(ctx).Take(&saved, "singleton=1").Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return false, e
		}
		savedFound = e == nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if savedFound {
			return false, ErrLifecycleUncertain
		}
		return false, nil
	}
	if err != nil || !key.valid() || !current.Complete || current.KeyVersion != key.Version() || current.KeyID != key.ID() {
		return false, ErrNewsletterIdentityUnavailable
	}
	if savedFound && (saved.KeyID != key.ID() || saved.KeyVersion != key.Version() || saved.JournalCheckpoint > current.Checkpoint) {
		return false, ErrLifecycleUncertain
	}
	var pending int64
	if err := j.store.DB.WithContext(ctx).Table("newsletter_identity_intents").Count(&pending).Error; err != nil || pending != 0 {
		return false, ErrLifecycleUncertain
	}
	return true, nil
}

func newsletterContactProfileEmpty(user User) bool {
	return user.Password == "" && user.EmailVerifiedAt == nil && user.EmailChangedAt == nil && user.NewsletterOwnershipEpoch == nil && user.FirstName == "" && user.LastName == "" && user.PreferencesJSON == nil && user.Sex == nil && user.Birthdate == nil && user.Allergens == nil && user.Diet == nil && user.Dislikes == nil && user.LastMeal == nil && user.CookingEfforts == nil && user.CookingTechnique == nil && (user.HasPref == nil || !*user.HasPref)
}

// RestoreNewsletterIdentities runs only on the unpublished working copy under
// the operator's journal lock. It never guesses a current full account's profile
// or mailbox from an old contact snapshot. The retained source stays read-only.
func (j *LifecycleJournal) RestoreNewsletterIdentities(ctx context.Context, key *NewsletterIdentityKey) error {
	if err := j.InitializeNewsletterIdentity(ctx, key); err != nil {
		return err
	}
	j.app.AccountWork.Lock()
	err := j.restoreNewsletterIdentitiesOwned(ctx, key)
	if err == nil {
		// The snapshot may predate expiry. Drain ended account work on the
		// unpublished copy before a successful physical-cleanup receipt.
		now := time.Now().UTC()
		for {
			if err = ctx.Err(); err != nil {
				break
			}
			changed := false
			err = j.app.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var sweepErr error
				changed, sweepErr = SweepExpiredAccountProofSlice(ctx, tx, now)
				return sweepErr
			})
			if err != nil || !changed {
				break
			}
		}
	}
	j.app.AccountWork.Unlock()
	if err != nil {
		return err
	}
	if err := j.CompactNewsletterIdentityCheckpoints(ctx); err != nil {
		return err
	}
	return j.CleanupNewsletterFiles(ctx)
}

func (j *LifecycleJournal) restoreNewsletterIdentitiesOwned(ctx context.Context, key *NewsletterIdentityKey) error {
	if err := j.CheckReady(ctx); err != nil {
		return err
	}
	var binding newsletterIdentityJournalKey
	if err := j.store.DB.WithContext(ctx).Take(&binding, "singleton=1").Error; err != nil {
		return err
	}
	var records []NewsletterIdentityRecord
	if err := j.store.DB.WithContext(ctx).Table("newsletter_identity_registry").Order("user_id").Find(&records).Error; err != nil {
		return err
	}
	known := make(map[int64]NewsletterIdentityRecord, len(records))
	for _, record := range records {
		if _, _, err := newsletterRecordPayload(record); err != nil {
			return err
		}
		known[record.UserID] = record
	}
	var users []User
	if err := j.app.DB.WithContext(ctx).Find(&users).Error; err != nil {
		return err
	}
	present := make(map[int64]User, len(users))
	for _, user := range users {
		present[user.ID] = user
		record, found := known[user.ID]
		if user.IsNewsletterOnly && (!found || record.Class != "contact") {
			return ErrLifecycleUncertain
		}
		if !found {
			continue
		}
		var incarnation string
		if err := j.app.SQL.QueryRowContext(ctx, "SELECT incarnation FROM account_incarnations WHERE user_id=?", user.ID).Scan(&incarnation); err != nil || incarnation != record.Incarnation {
			return ErrLifecycleConflict
		}
		if record.Class == "app" {
			if user.IsNewsletterOnly || user.NewsletterMinimised || user.NewsletterOwnershipEpoch == nil || *user.NewsletterOwnershipEpoch != record.OwnershipEpoch {
				return ErrLifecycleConflict
			}
		} else {
			if !user.IsNewsletterOnly || !newsletterContactProfileEmpty(user) {
				return ErrLifecycleConflict
			}
			if user.NewsletterMinimised {
				var match NewsletterIdentityMatch
				if err := j.app.DB.WithContext(ctx).Take(&match, "user_id=?", user.ID).Error; err != nil || match.Incarnation != record.Incarnation || match.KeyVersion != record.KeyVersion || match.AddressTag != record.AddressTag {
					return ErrLifecycleConflict
				}
			} else {
				tag, err := key.Tag(user.Email)
				if err != nil || tag != record.AddressTag {
					return ErrLifecycleConflict
				}
			}
		}
	}
	for _, record := range records {
		if _, found := present[record.UserID]; !found && record.Class == "app" {
			return ErrLifecycleConflict
		}
	}
	return j.app.NewsletterTransaction(ctx, j, func(tx *gorm.DB) error {
		// Nothing from the snapshot can dispatch or prove newsletter consent again.
		for _, query := range []string{
			"DELETE FROM newsletter_requests",
			"DELETE FROM signup_requests",
			"DELETE FROM email_events WHERE provider_id IN (SELECT provider_id FROM email_outbox WHERE key LIKE 'newsletter/%')",
			"DELETE FROM email_outbox WHERE key LIKE 'newsletter/%'",
			"DELETE FROM newsletter_proofs WHERE purpose='confirm'",
		} {
			if err := tx.Exec(query).Error; err != nil {
				return err
			}
		}
		floor := int64(0)
		for _, record := range records {
			floor = max(floor, record.DecisionOrder)
		}
		if err := tx.Exec("UPDATE newsletter_sequence SET value=MAX(value,?) WHERE singleton=1", floor).Error; err != nil {
			return err
		}
		// Full accounts with no newsletter-only origin retain their existing reset
		// rule, without acquiring a registry entry or an invented ownership epoch.
		if err := tx.Exec("UPDATE newsletter_subscriptions SET status='pending',revision=revision+1,proved_order=(SELECT value+1 FROM newsletter_sequence WHERE singleton=1) WHERE user_id IN (SELECT user_id FROM users WHERE isNewsletterOnly=0 AND newsletter_ownership_epoch IS NULL)").Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE newsletter_sequence SET value=value+1 WHERE singleton=1").Error; err != nil {
			return err
		}
		for _, record := range records {
			status, revision, order := record.Status, record.Revision, record.DecisionOrder
			if status == "confirmed" {
				status = "pending"
				revision++
				if err := tx.Raw("UPDATE newsletter_sequence SET value=value+1 WHERE singleton=1 RETURNING value").Row().Scan(&order); err != nil {
					return err
				}
			}
			if record.Class == "contact" {
				if err := tx.Exec("INSERT INTO newsletter_identity_matches(user_id,incarnation,key_version,address_tag) VALUES(?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET incarnation=excluded.incarnation,key_version=excluded.key_version,address_tag=excluded.address_tag", record.UserID, record.Incarnation, record.KeyVersion, record.AddressTag).Error; err != nil {
					return err
				}
				if _, found := present[record.UserID]; !found {
					if err := tx.Exec("INSERT INTO users(user_id,firstname,lastname,email,email_key,password,username,isNewsletterOnly,newsletter_minimised) VALUES(?,'','',NULL,NULL,'',NULL,1,1)", record.UserID).Error; err != nil {
						return err
					}
				} else if err := tx.Exec("UPDATE users SET newsletter_minimised=1,email=NULL,email_key=NULL,username=NULL WHERE user_id=?", record.UserID).Error; err != nil {
					return err
				}
			}
			if record.NoticeVersion == nil && record.Revision == 0 {
				if err := tx.Exec("DELETE FROM newsletter_subscriptions WHERE user_id=?", record.UserID).Error; err != nil {
					return err
				}
			} else {
				var mailbox any
				if record.Class == "app" {
					mailbox = present[record.UserID].EmailKey
				}
				var withdrawn any
				if status == "unsubscribed" {
					withdrawn = record.DecisionUnix
				}
				if err := tx.Exec(`INSERT INTO newsletter_subscriptions(user_id,email_key,status,locale,consent_version,source,requested_unix,confirmed_unix,withdrawn_unix,revision,proved_order,decision_unix)
VALUES(?,?,?,NULL,?,NULL,NULL,NULL,?,?,?,?) ON CONFLICT(user_id) DO UPDATE SET email_key=excluded.email_key,status=excluded.status,locale=NULL,consent_version=excluded.consent_version,source=NULL,requested_unix=NULL,confirmed_unix=NULL,withdrawn_unix=excluded.withdrawn_unix,revision=excluded.revision,proved_order=excluded.proved_order,decision_unix=excluded.decision_unix`, record.UserID, mailbox, status, record.NoticeVersion, withdrawn, revision, order, record.DecisionUnix).Error; err != nil {
					return err
				}
			}
			TrackNewsletterIdentity(tx, record.UserID)
		}
		if err := preserveRestoredWithdrawals(ctx, tx, key, known); err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO sqlite_sequence(name,seq) SELECT 'users',? WHERE NOT EXISTS(SELECT 1 FROM sqlite_sequence WHERE name='users')", binding.HighWater).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE sqlite_sequence SET seq=MAX(seq,?) WHERE name='users'", binding.HighWater).Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM newsletter_identity_commits").Error; err != nil {
			return err
		}
		return tx.Exec("UPDATE newsletter_identity_namespace SET physical_cleanup_pending=1,legacy_vacuum_pending=1,journal_checkpoint=? WHERE singleton=1", binding.Checkpoint).Error
	})
}

// ValidateNewsletterIdentities refuses an old direct SQL copy at startup or
// before rights operations. Only explicit restore may reconcile older state.
func (j *LifecycleJournal) ValidateNewsletterIdentities(ctx context.Context) error {
	j.app.AccountWork.RLock()
	defer j.app.AccountWork.RUnlock()
	_, bound, err := j.newsletterIdentityBinding(ctx)
	if err != nil {
		return err
	}
	if !bound {
		return nil
	}
	if err := j.CheckReady(ctx); err != nil {
		return err
	}
	var records []NewsletterIdentityRecord
	if err := j.store.DB.WithContext(ctx).Table("newsletter_identity_registry").Find(&records).Error; err != nil {
		return err
	}
	known := make(map[int64]bool, len(records))
	for _, record := range records {
		if err := j.validateNewsletterIdentityPostState(ctx, j.app.DB, record); err != nil {
			return err
		}
		known[record.UserID] = true
	}
	var ids []int64
	if err := j.app.DB.WithContext(ctx).Model(&User{}).Where("isNewsletterOnly=1 OR newsletter_ownership_epoch IS NOT NULL").Pluck("user_id", &ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		if !known[id] {
			return ErrLifecycleUncertain
		}
	}
	return nil
}

type restoredNewsletterProof struct {
	Hash                      string
	UserID                    int64
	EmailKey                  *string
	Revision                  int64
	ConsumedUnix, ExpiresUnix *int64
	IdentityIncarnation       *string
	IdentityKeyVersion        *int
	IdentityAddressTag        *string
}

// Retain only a snapshot's recognizable unchanged-mailbox withdrawal hashes.
// A superseded revision becomes an acknowledgment, never renewed authority.
// Current unconsumed authority survives the administrative restore reset.
func preserveRestoredWithdrawals(ctx context.Context, tx *gorm.DB, key *NewsletterIdentityKey, known map[int64]NewsletterIdentityRecord) error {
	after := ""
	for {
		var proofs []restoredNewsletterProof
		if err := tx.WithContext(ctx).Table("newsletter_proofs").Where("purpose='unsubscribe' AND hash>?", after).Order("hash").Limit(100).Find(&proofs).Error; err != nil {
			return err
		}
		if len(proofs) == 0 {
			return nil
		}
		for _, proof := range proofs {
			after = proof.Hash
			var user User
			if err := tx.First(&user, proof.UserID).Error; err != nil {
				return err
			}
			var incarnation string
			if err := tx.Raw("SELECT incarnation FROM account_incarnations WHERE user_id=?", user.ID).Row().Scan(&incarnation); err != nil {
				return err
			}
			record, tracked := known[user.ID]
			tag := ""
			if tracked && record.Class == "contact" {
				tag = record.AddressTag
			} else {
				var err error
				tag, err = key.Tag(user.Email)
				if err != nil {
					return err
				}
			}
			matches := true
			if proof.EmailKey != nil {
				actual, err := key.Tag(*proof.EmailKey)
				matches = err == nil && actual == tag
			}
			if proof.IdentityIncarnation != nil {
				matches = matches && *proof.IdentityIncarnation == incarnation && proof.IdentityKeyVersion != nil && *proof.IdentityKeyVersion == key.Version() && proof.IdentityAddressTag != nil && *proof.IdentityAddressTag == tag
			} else if proof.EmailKey == nil {
				matches = false
			}
			if !matches {
				if err := tx.Exec("DELETE FROM newsletter_proofs WHERE hash=?", proof.Hash).Error; err != nil {
					return err
				}
				continue
			}
			current := tracked && proof.Revision == record.Revision && record.Status != "unsubscribed" && proof.ConsumedUnix == nil && (proof.ExpiresUnix == nil || *proof.ExpiresUnix > time.Now().Unix())
			updates := map[string]any{"email_key": nil, "identity_incarnation": incarnation, "identity_key_version": key.Version(), "identity_address_tag": tag, "proposal": ""}
			if current {
				var revision int64
				if err := tx.Raw("SELECT revision FROM newsletter_subscriptions WHERE user_id=?", user.ID).Row().Scan(&revision); err != nil {
					return err
				}
				updates["revision"] = revision
			} else {
				updates["consumed_unix"] = gorm.Expr("COALESCE(consumed_unix,?)", time.Now().Unix())
			}
			if err := tx.Table("newsletter_proofs").Where("hash=?", proof.Hash).Updates(updates).Error; err != nil {
				return err
			}
		}
	}
}
