package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/LowGok/my-chef/internal/notifications"
	"gorm.io/gorm"
)

const reminderRegistrationMaxAge = 30 * 24 * time.Hour

type reminderOccurrence struct {
	ID          string
	Incarnation string
	Purpose     string
	TargetWeek  string
	Scheduled   int64
	Deadline    int64
	Revision    int64
}

type reminderDispatch struct {
	Reference  string
	Occurrence string
	FamilyID   string
	Attempts   int
}

type reminderRegistration struct {
	Provider    string
	TokenTag    []byte
	TokenCipher []byte
}

const reminderFamilyActive = ` FROM reminder_installations i
JOIN native_families f ON f.id=i.family_id
JOIN sessions a ON a.hash=f.anchor_hash
JOIN account_incarnations o ON o.incarnation=i.incarnation AND o.user_id=a.user_id
WHERE i.incarnation=? AND f.platform=i.platform AND f.expires_unix>? AND a.expires_unix>? AND i.refreshed_unix>?`

func (s *ReminderSettings) evaluateReminderPreferences(ctx context.Context) error {
	return s.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		at := s.now()
		var records []struct {
			Incarnation string
			UserID      int64
		}
		err := tx.Raw(`SELECT p.incarnation,a.user_id FROM reminder_preferences p JOIN account_incarnations a ON a.incarnation=p.incarnation
WHERE (p.current_enabled=1 OR p.advance_enabled=1) AND p.next_evaluation<=?
ORDER BY p.next_evaluation,p.incarnation LIMIT 64`, at.Unix()).Scan(&records).Error
		if err != nil {
			return err
		}
		for _, record := range records {
			p, err := loadReminderPreferences(tx, record.Incarnation)
			if err != nil {
				return err
			}
			zone, err := time.LoadLocation(p.Zone)
			if err != nil {
				// A removed tzdata alias must not block unrelated households.
				s.zoneFailures.Add(1)
				if err := tx.Exec("UPDATE reminder_preferences SET next_evaluation=? WHERE incarnation=?", at.Add(time.Hour).Unix(), record.Incarnation).Error; err != nil {
					return err
				}
				continue
			}
			upsert := func(purpose string, o notifications.Occurrence) error {
				if !o.Due(at) {
					return nil
				}
				return tx.Exec(`INSERT INTO reminder_occurrences(id,incarnation,purpose,target_week,scheduled,deadline,revision)
VALUES(lower(hex(randomblob(16))),?,?,?,?,?,?) ON CONFLICT(incarnation,purpose,target_week)
DO UPDATE SET scheduled=excluded.scheduled,deadline=excluded.deadline,revision=excluded.revision,next_check=0
WHERE reminder_occurrences.frozen=0 AND (reminder_occurrences.scheduled<>excluded.scheduled OR
reminder_occurrences.deadline<>excluded.deadline OR reminder_occurrences.revision<>excluded.revision)`,
					record.Incarnation, purpose, o.Week, o.Scheduled.Unix(), o.Deadline.Unix(), p.Revision).Error
			}
			if p.Current {
				if err := upsert("current", notifications.CurrentWeek(at, zone)); err != nil {
					return err
				}
			}
			if p.Advance {
				premium, err := s.reminderPremium(ctx, tx, record.UserID, record.Incarnation, at)
				if err != nil {
					return err
				}
				if premium {
					if err := upsert("advance", notifications.AdvanceWeek(at, zone, time.Weekday(p.AdvanceDay), p.AdvanceMinute)); err != nil {
						return err
					}
				}
			}
			if err := tx.Exec("UPDATE reminder_preferences SET next_evaluation=? WHERE incarnation=?", at.Add(time.Minute).Unix(), record.Incarnation).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// RecoverReminderDispatches is called only under the process-wide app lock.
// A crash after the send marker is indistinguishable from a lost reply.
func (s *ReminderSettings) RecoverReminderDispatches(ctx context.Context) error {
	return s.store.DB.WithContext(ctx).Exec("UPDATE reminder_dispatches SET state='uncertain' WHERE state='sending'").Error
}

type ReminderHealth struct {
	Queued, Retrying, Accepted, Uncertain, Rejected, Expired, OldestDueSeconds int64
	ZoneFailures                                                               int64
}

// ReminderHealth exposes aggregate queue states, without any account, token,
// provider response, or notification reference.
func (s *ReminderSettings) ReminderHealth(ctx context.Context) (ReminderHealth, error) {
	var health ReminderHealth
	var oldest int64
	at := s.now().Unix()
	err := s.store.DB.WithContext(ctx).Raw(`SELECT
count(*) FILTER (WHERE d.state='pending' AND o.deadline>?),
count(*) FILTER (WHERE d.state='pending' AND d.attempts>0 AND o.deadline>?),
count(*) FILTER (WHERE d.state='accepted'),count(*) FILTER (WHERE d.state='uncertain'),
count(*) FILTER (WHERE d.state='permanent'),count(*) FILTER (WHERE d.state='pending' AND o.deadline<=?),
coalesce(min(d.next_attempt) FILTER (WHERE d.state='pending' AND d.next_attempt<=? AND o.deadline>?),?)
FROM reminder_dispatches d JOIN reminder_occurrences o ON o.id=d.occurrence`, at, at, at, at, at, at).Row().Scan(
		&health.Queued, &health.Retrying, &health.Accepted, &health.Uncertain, &health.Rejected, &health.Expired, &oldest)
	health.OldestDueSeconds = max(0, at-oldest)
	health.ZoneFailures = s.zoneFailures.Load()
	return health, err
}

func (s *ReminderSettings) claimReminder(ctx context.Context) (notifications.Delivery, reminderDispatch, []byte, error) {
	var delivery notifications.Delivery
	var dispatch reminderDispatch
	var tokenTag []byte
	err := s.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		at := s.now()
		// Retain the current and prior weeks for deduplication. Older expired
		// rows cannot authorize a tap or be revived by an ordinary zone edit.
		if err := tx.Exec(`DELETE FROM reminder_occurrences WHERE id IN (
SELECT id FROM reminder_occurrences WHERE deadline<? LIMIT 256)`, at.Add(-14*24*time.Hour).Unix()).Error; err != nil {
			return err
		}
		mark := func(o reminderOccurrence, candidate reminderDispatch, registration reminderRegistration, token []byte) error {
			defer clear(token)
			if err := tx.Exec("UPDATE reminder_dispatches SET state='sending',attempts=attempts+1 WHERE reference=? AND state='pending'", candidate.Reference).Error; err != nil {
				return err
			}
			candidate.Attempts++
			dispatch = candidate
			delivery = notifications.Delivery{Provider: registration.Provider, Token: string(token), Reference: candidate.Reference, Deadline: time.Unix(o.Deadline, 0)}
			tokenTag = registration.TokenTag
			return nil
		}
		var due []reminderDispatch
		if err := tx.Raw(`SELECT d.reference,d.occurrence,d.family_id,d.attempts FROM reminder_dispatches d JOIN reminder_occurrences o ON o.id=d.occurrence
WHERE d.state='pending' AND d.next_attempt<=? AND o.deadline>? ORDER BY d.next_attempt,d.family_id,d.reference LIMIT 64`,
			at.Unix(), at.Unix()).Scan(&due).Error; err != nil {
			return err
		}
		for _, candidate := range due {
			var o reminderOccurrence
			if err := tx.Raw("SELECT id,incarnation,purpose,target_week,scheduled,deadline,revision FROM reminder_occurrences WHERE id=?", candidate.Occurrence).Scan(&o).Error; err != nil {
				return err
			}
			active, err := s.reminderEligible(ctx, tx, o, at, true)
			if err != nil {
				return err
			}
			var registration reminderRegistration
			if active {
				err = tx.Raw("SELECT i.provider,i.token_tag,i.token_cipher"+reminderFamilyActive+" AND i.family_id=?",
					o.Incarnation, at.Unix(), at.Unix(), at.Add(-reminderRegistrationMaxAge).Unix(), candidate.FamilyID).Row().Scan(&registration.Provider, &registration.TokenTag, &registration.TokenCipher)
				if errors.Is(err, sql.ErrNoRows) {
					active = false
				} else if err != nil {
					return err
				}
			}
			var token []byte
			if active && !s.providerEnabled(registration.Provider) {
				active = false
			}
			if active {
				token, err = s.vault.Open(nil, nil, registration.TokenCipher, []byte(o.Incarnation+":"+candidate.FamilyID+":"+registration.Provider))
				if err != nil {
					active = false
				}
			}
			if !active {
				if err := tx.Exec("UPDATE reminder_dispatches SET state='suppressed' WHERE reference=?", candidate.Reference).Error; err != nil {
					return err
				}
				continue
			}
			return mark(o, candidate, registration, token)
		}
		var pending []reminderOccurrence
		if err := tx.Raw(`SELECT o.id,o.incarnation,o.purpose,o.target_week,o.scheduled,o.deadline,o.revision
FROM reminder_occurrences o JOIN reminder_preferences p ON p.incarnation=o.incarnation AND p.revision=o.revision
WHERE o.frozen=0 AND o.next_check<=? AND o.scheduled<=? AND o.deadline>?
AND NOT EXISTS(SELECT 1 FROM meals m JOIN account_incarnations a ON a.user_id=m.creator_id AND a.incarnation=o.incarnation WHERE m.creationdate>=o.target_week AND m.creationdate<date(o.target_week,'+7 days'))
AND EXISTS(SELECT 1 FROM reminder_installations i JOIN native_families f ON f.id=i.family_id
JOIN sessions anchor ON anchor.hash=f.anchor_hash WHERE i.incarnation=o.incarnation
AND f.expires_unix>? AND anchor.expires_unix>? AND i.refreshed_unix>?)
ORDER BY o.next_check,o.scheduled,o.id LIMIT 64`, at.Unix(), at.Unix(), at.Unix(), at.Unix(), at.Unix(), at.Add(-reminderRegistrationMaxAge).Unix()).Scan(&pending).Error; err != nil {
			return err
		}
		for _, o := range pending {
			active, err := s.reminderEligible(ctx, tx, o, at, true)
			if err != nil {
				return err
			}
			var first reminderDispatch
			var registration reminderRegistration
			var token []byte
			if active {
				var registrations []struct {
					FamilyID    string
					Provider    string
					TokenTag    []byte
					TokenCipher []byte
				}
				if err := tx.Raw("SELECT i.family_id,i.provider,i.token_tag,i.token_cipher"+reminderFamilyActive+" ORDER BY i.family_id",
					o.Incarnation, at.Unix(), at.Unix(), at.Add(-reminderRegistrationMaxAge).Unix()).Scan(&registrations).Error; err != nil {
					return err
				}
				for _, device := range registrations {
					if !s.providerEnabled(device.Provider) {
						continue
					}
					secret, err := s.vault.Open(nil, nil, device.TokenCipher, []byte(o.Incarnation+":"+device.FamilyID+":"+device.Provider))
					if err != nil {
						continue
					}
					defer clear(secret)
					var reference string
					if err := tx.Raw("INSERT INTO reminder_dispatches(reference,occurrence,family_id,state,next_attempt) VALUES(lower(hex(randomblob(16))),?,?,'pending',?) RETURNING reference",
						o.ID, device.FamilyID, at.Unix()).Row().Scan(&reference); err != nil {
						return err
					}
					if first.Reference == "" {
						first = reminderDispatch{Reference: reference, Occurrence: o.ID, FamilyID: device.FamilyID}
						registration, token = reminderRegistration{Provider: device.Provider, TokenTag: device.TokenTag, TokenCipher: device.TokenCipher}, secret
					} else {
						clear(secret)
					}
				}
			}
			if first.Reference == "" {
				if err := tx.Exec("UPDATE reminder_occurrences SET next_check=? WHERE id=?", at.Add(time.Minute).Unix(), o.ID).Error; err != nil {
					return err
				}
				continue
			}
			// Freeze only the occurrence whose first sending marker is being
			// committed. Other accounts can still add devices before their send.
			if err := tx.Exec("UPDATE reminder_occurrences SET frozen=1 WHERE id=?", o.ID).Error; err != nil {
				return err
			}
			return mark(o, first, registration, token)
		}
		return nil
	})
	return delivery, dispatch, tokenTag, err
}

func (s *ReminderSettings) reminderEligible(ctx context.Context, tx *gorm.DB, o reminderOccurrence, at time.Time, sending bool) (bool, error) {
	if at.Unix() >= o.Deadline || at.Unix() < o.Scheduled {
		return false, nil
	}
	p, err := loadReminderPreferences(tx, o.Incarnation)
	if err != nil {
		return false, err
	}
	if p.Revision == 0 || (o.Purpose == "advance" && !p.Advance) || (o.Purpose == "current" && !p.Current) {
		return false, nil
	}
	var user int64
	err = tx.Raw(`SELECT a.user_id FROM account_incarnations a JOIN users u ON u.user_id=a.user_id
WHERE a.incarnation=? AND u.email_verified_at>0`, o.Incarnation).Row().Scan(&user)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	premium := false
	if o.Purpose == "advance" {
		premium, err = s.reminderPremium(ctx, tx, user, o.Incarnation, at)
		if err != nil {
			return false, err
		}
	}
	zone, err := time.LoadLocation(p.Zone)
	if err != nil {
		return false, nil
	}
	if !notifications.OpenWeekAllowed(at, zone, o.TargetWeek, o.Purpose == "advance", premium) {
		return false, nil
	}
	if !sending {
		return true, nil
	}
	if p.Revision != o.Revision {
		var current notifications.Occurrence
		if o.Purpose == "advance" {
			current = notifications.AdvanceWeek(at, zone, time.Weekday(p.AdvanceDay), p.AdvanceMinute)
		} else {
			current = notifications.CurrentWeek(at, zone)
		}
		if current.Scheduled.Unix() != o.Scheduled || current.Deadline.Unix() != o.Deadline {
			return false, nil
		}
	}
	var planned bool
	err = tx.Raw(`SELECT EXISTS(SELECT 1 FROM meals WHERE creator_id=? AND creationdate>=? AND creationdate<date(?,'+7 days'))`, user, o.TargetWeek, o.TargetWeek).Row().Scan(&planned)
	return !planned, err
}

func (s *ReminderSettings) RunReminderStep(ctx context.Context, send notifications.Send) (bool, error) {
	if send == nil {
		return false, ErrReminderInput
	}
	if err := s.evaluateReminderPreferences(ctx); err != nil {
		return false, err
	}
	delivery, dispatch, tag, err := s.claimReminder(ctx)
	if err != nil || dispatch.Reference == "" {
		return false, err
	}
	call, cancel := context.WithTimeout(ctx, 15*time.Second)
	result := send(call, delivery)
	cancel()
	now := s.now()
	state := "uncertain"
	next := now
	switch result.State {
	case notifications.Accepted:
		state = "accepted"
	case notifications.Temporary:
		next = now.Add(notifications.RetryDelay(dispatch.Attempts, result.RetryAfter))
		if dispatch.Attempts < 5 && next.Before(delivery.Deadline) {
			state = "pending"
		} else {
			state = "permanent"
		}
	case notifications.Permanent, notifications.InvalidRegistration:
		state = "permanent"
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	err = s.store.DB.WithContext(finish).Transaction(func(tx *gorm.DB) error {
		if result.State == notifications.InvalidRegistration {
			if err := tx.Exec("DELETE FROM reminder_installations WHERE family_id=? AND token_tag=?", dispatch.FamilyID, tag).Error; err != nil {
				return err
			}
		}
		return tx.Exec("UPDATE reminder_dispatches SET state=?,next_attempt=? WHERE reference=? AND state='sending'",
			state, next.Unix(), dispatch.Reference).Error
	})
	return true, err
}

func (s *ReminderSettings) OpenReminder(ctx context.Context, actor PersonalActor, reference string) (string, error) {
	if !validPersonalID(reference) {
		return "", ErrReminderMissing
	}
	var week string
	err := s.store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		at := s.now()
		owner, err := personalAuthority(tx, actor, at)
		if err != nil {
			return err
		}
		var o reminderOccurrence
		err = tx.Raw(`SELECT o.id,o.incarnation,o.purpose,o.target_week,o.scheduled,o.deadline,o.revision
FROM reminder_occurrences o JOIN reminder_dispatches d ON d.occurrence=o.id
WHERE o.incarnation=? AND d.reference=? AND d.state IN ('sending','accepted','uncertain')`, owner, reference).Row().
			Scan(&o.ID, &o.Incarnation, &o.Purpose, &o.TargetWeek, &o.Scheduled, &o.Deadline, &o.Revision)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrReminderMissing
		}
		if err != nil {
			return err
		}
		ok, err := s.reminderEligible(ctx, tx, o, at, false)
		if err != nil {
			return err
		}
		if !ok {
			return ErrReminderMissing
		}
		week = o.TargetWeek
		return nil
	})
	return week, err
}
