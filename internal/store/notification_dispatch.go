package store

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
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
	IdentityLane     string
	Environment      string
	IdentityRevision int64
	Reference        string
	Occurrence       string
	FamilyID         string
	Attempts         int
}

type reminderRegistration struct {
	IdentityLane     string
	Environment      string
	IdentityRevision int64
	Provider         string
	TokenTag         []byte
	TokenCipher      []byte
}

const reminderFamilyActive = ` FROM reminder_installations i
JOIN native_families f ON f.id=i.family_id
JOIN sessions a ON a.hash=f.anchor_hash
JOIN account_incarnations o ON o.incarnation=i.incarnation AND o.user_id=a.user_id
WHERE (i.identity_lane='preview' OR EXISTS(SELECT 1 FROM native_identity_policy p WHERE p.id=1 AND p.active=1 AND p.revision=i.identity_revision)) AND i.identity_lane=f.identity_lane AND i.identity_revision=f.identity_revision AND i.incarnation=? AND f.platform=i.platform AND f.expires_unix>? AND a.expires_unix>? AND i.refreshed_unix>?`

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
			zone, err := s.loadLocation(p.Zone)
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
			if err := tx.Exec("UPDATE reminder_dispatches SET state='sending',attempts=attempts+1,submission_deadline=? WHERE reference=? AND state='pending'", o.Deadline, candidate.Reference).Error; err != nil {
				return err
			}
			candidate.Attempts++
			dispatch = candidate
			delivery = notifications.Delivery{IdentityLane: registration.IdentityLane, Environment: registration.Environment, Provider: registration.Provider, Token: string(token), Reference: candidate.Reference, Deadline: time.Unix(o.Deadline, 0)}
			tokenTag = registration.TokenTag
			return nil
		}
		var due []reminderDispatch
		if err := tx.Raw(`SELECT d.reference,d.occurrence,d.family_id,d.attempts,d.identity_lane,d.environment,d.identity_revision FROM reminder_dispatches d JOIN reminder_occurrences o ON o.id=d.occurrence
WHERE d.state='pending' AND d.next_attempt<=? AND o.deadline>? ORDER BY d.schedule_checked,d.next_attempt,d.family_id,d.reference LIMIT 64`,
			at.Unix(), at.Unix()).Scan(&due).Error; err != nil {
			return err
		}
		for _, candidate := range due {
			var o reminderOccurrence
			if err := tx.Raw("SELECT id,incarnation,purpose,target_week,scheduled,deadline,revision FROM reminder_occurrences WHERE id=?", candidate.Occurrence).Scan(&o).Error; err != nil {
				return err
			}
			window, active, err := s.reminderWindow(ctx, tx, o, at, true, false)
			if err != nil {
				return err
			}
			if active && at.Before(window.Scheduled) {
				// Do not replace the provider retry floor with a civil time.
				// Another zone-rules update may move this window earlier again.
				if err := tx.Exec("UPDATE reminder_dispatches SET schedule_checked=? WHERE reference=?", at.UnixNano(), candidate.Reference).Error; err != nil {
					return err
				}
				continue
			}
			var registration reminderRegistration
			if active {
				err = tx.Raw("SELECT i.provider,i.token_tag,i.token_cipher,i.identity_lane,i.environment,i.identity_revision"+reminderFamilyActive+" AND i.family_id=? AND i.identity_lane=? AND i.environment=? AND i.identity_revision=?",
					o.Incarnation, at.Unix(), at.Unix(), at.Add(-reminderRegistrationMaxAge).Unix(), candidate.FamilyID, candidate.IdentityLane, candidate.Environment, candidate.IdentityRevision).Row().Scan(&registration.Provider, &registration.TokenTag, &registration.TokenCipher, &registration.IdentityLane, &registration.Environment, &registration.IdentityRevision)
				if errors.Is(err, sql.ErrNoRows) {
					active = false
				} else if err != nil {
					return err
				}
			}
			var token []byte
			if active && !s.identityProviderEnabled(registration.IdentityLane, registration.Environment, registration.Provider) {
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
			o.Deadline = window.Deadline.Unix()
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
			window, active, err := s.reminderWindow(ctx, tx, o, at, false, false)
			if err != nil {
				return err
			}
			active = active && window.Due(at)
			var first reminderDispatch
			var registration reminderRegistration
			var token []byte
			if active {
				var registrations []struct {
					IdentityLane     string
					Environment      string
					IdentityRevision int64
					FamilyID         string
					Provider         string
					TokenTag         []byte
					TokenCipher      []byte
				}
				if err := tx.Raw("SELECT i.family_id,i.provider,i.token_tag,i.token_cipher,i.identity_lane,i.environment,i.identity_revision"+reminderFamilyActive+" ORDER BY i.family_id",
					o.Incarnation, at.Unix(), at.Unix(), at.Add(-reminderRegistrationMaxAge).Unix()).Scan(&registrations).Error; err != nil {
					return err
				}
				for _, device := range registrations {
					if !s.identityProviderEnabled(device.IdentityLane, device.Environment, device.Provider) {
						continue
					}
					secret, err := s.vault.Open(nil, nil, device.TokenCipher, []byte(o.Incarnation+":"+device.FamilyID+":"+device.Provider))
					if err != nil {
						continue
					}
					defer clear(secret)
					var reference string
					if err := tx.Raw("INSERT INTO reminder_dispatches(reference,occurrence,family_id,state,next_attempt,identity_lane,environment,identity_revision) VALUES(lower(hex(randomblob(16))),?,?,'pending',?,?,?,?) RETURNING reference",
						o.ID, device.FamilyID, at.Unix(), device.IdentityLane, device.Environment, device.IdentityRevision).Row().Scan(&reference); err != nil {
						return err
					}
					if first.Reference == "" {
						first = reminderDispatch{IdentityLane: device.IdentityLane, Environment: device.Environment, IdentityRevision: device.IdentityRevision, Reference: reference, Occurrence: o.ID, FamilyID: device.FamilyID}
						registration, token = reminderRegistration{IdentityLane: device.IdentityLane, Environment: device.Environment, IdentityRevision: device.IdentityRevision, Provider: device.Provider, TokenTag: device.TokenTag, TokenCipher: device.TokenCipher}, secret
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

func (s *ReminderSettings) reminderWindow(ctx context.Context, tx *gorm.DB, o reminderOccurrence, at time.Time, frozen, opening bool) (notifications.Occurrence, bool, error) {
	var window notifications.Occurrence
	if at.Unix() >= o.Deadline || at.Unix() < o.Scheduled {
		return window, false, nil
	}
	p, err := loadReminderPreferences(tx, o.Incarnation)
	if err != nil {
		return window, false, err
	}
	if p.Revision == 0 || (o.Purpose == "advance" && !p.Advance) || (o.Purpose == "current" && !p.Current) {
		return window, false, nil
	}
	var user int64
	err = tx.Raw(`SELECT a.user_id FROM account_incarnations a JOIN users u ON u.user_id=a.user_id
WHERE a.incarnation=? AND u.email_verified_at>0`, o.Incarnation).Row().Scan(&user)
	if errors.Is(err, sql.ErrNoRows) {
		return window, false, nil
	}
	if err != nil {
		return window, false, err
	}
	premium := false
	if o.Purpose == "advance" {
		premium, err = s.reminderPremium(ctx, tx, user, o.Incarnation, at)
		if err != nil {
			return window, false, err
		}
	}
	zone, err := s.loadLocation(p.Zone)
	if err != nil {
		return window, false, nil
	}
	if !notifications.OpenWeekAllowed(at, zone, o.TargetWeek, o.Purpose == "advance", premium) {
		return window, false, nil
	}
	if opening {
		return window, true, nil
	}
	// Recompute immediately before the send marker. Zone rules can change
	// without a preference change. An unfrozen row is updated by evaluation.
	window = notifications.CurrentWeek(at, zone)
	if o.Purpose == "advance" {
		window = notifications.AdvanceWeek(at, zone, time.Weekday(p.AdvanceDay), p.AdvanceMinute)
	}
	moved := window.Scheduled.Unix() != o.Scheduled || window.Deadline.Unix() != o.Deadline
	if moved && (!frozen || p.Revision != o.Revision) {
		return window, false, nil
	}
	if frozen && window.Deadline.Unix() > o.Deadline {
		// Keep the original cutoff for an already started batch. Move only
		// pending copies; accepted or uncertain copies never enter this path.
		window.Deadline = time.Unix(o.Deadline, 0)
	}
	if !at.Before(window.Deadline) || !window.Scheduled.Before(window.Deadline) {
		return window, false, nil
	}
	var planned bool
	err = tx.Raw(`SELECT EXISTS(SELECT 1 FROM meals WHERE creator_id=? AND creationdate>=? AND creationdate<date(?,'+7 days'))`, user, o.TargetWeek, o.TargetWeek).Row().Scan(&planned)
	return window, !planned, err
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
	return s.submitClaimedReminder(ctx, delivery, dispatch, tag, send)
}

func (s *ReminderSettings) submitClaimedReminder(ctx context.Context, delivery notifications.Delivery, dispatch reminderDispatch, tag []byte, send notifications.Send) (bool, error) {
	call, cancel := context.WithTimeout(ctx, 15*time.Second)
	var attempted, crossed atomic.Bool
	if dispatch.IdentityLane == "app" {
		call = notifications.WithSubmissionGate(call, func() (func(), bool) {
			if !attempted.CompareAndSwap(false, true) {
				return func() {}, false
			}
			s.store.NativeIdentityWork.RLock()
			policy, err := s.store.NativeIdentityPolicy(call)
			if err != nil || call.Err() != nil || !policy.Active || policy.Revision != dispatch.IdentityRevision || !s.identityProviderEnabled(dispatch.IdentityLane, dispatch.Environment, delivery.Provider) {
				s.store.NativeIdentityWork.RUnlock()
				return func() {}, false
			}
			crossed.Store(true)
			return s.store.NativeIdentityWork.RUnlock, true
		})
	}
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
	if dispatch.IdentityLane == "app" && !crossed.Load() {
		state = "suppressed"
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	err := s.store.DB.WithContext(finish).Transaction(func(tx *gorm.DB) error {
		if state == "pending" && dispatch.IdentityLane == "app" {
			var allowed bool
			if err := tx.Raw("SELECT active=1 AND revision=? FROM native_identity_policy WHERE id=1", dispatch.IdentityRevision).Row().Scan(&allowed); err != nil {
				return err
			}
			if !allowed {
				state = "suppressed"
			}
		}
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
		err = tx.Raw(`SELECT o.id,o.incarnation,o.purpose,o.target_week,o.scheduled,coalesce(d.submission_deadline,o.deadline),o.revision
FROM reminder_occurrences o JOIN reminder_dispatches d ON d.occurrence=o.id
WHERE o.incarnation=? AND d.reference=? AND d.state IN ('sending','accepted','uncertain')
AND (?=0 OR EXISTS(SELECT 1 FROM native_accesses access
JOIN native_families family ON family.id=access.family_id
WHERE access.hash=? AND family.identity_lane=d.identity_lane AND family.identity_revision=d.identity_revision
AND (family.identity_lane='preview' OR EXISTS(SELECT 1 FROM native_identity_policy policy
WHERE policy.id=1 AND policy.active=1 AND policy.revision=family.identity_revision))))`, owner, reference, actor.Native, actor.SessionHash).Row().
			Scan(&o.ID, &o.Incarnation, &o.Purpose, &o.TargetWeek, &o.Scheduled, &o.Deadline, &o.Revision)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrReminderMissing
		}
		if err != nil {
			return err
		}
		_, ok, err := s.reminderWindow(ctx, tx, o, at, false, true)
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
