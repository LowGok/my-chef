// Package billing stores provider-verified purchase state outside app snapshots.
// Provider adapters are responsible for interpreting purchase evidence.
package billing

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

type Store string
type Environment string
type Interval string
type RenewalState string

const (
	RenewalRenews RenewalState = "renews"
	RenewalEnds   RenewalState = "ends"
)

// RenewalObservation describes what the selling store last verified. It does
// not extend or revoke a paid period.
type RenewalObservation struct {
	State      RenewalState
	ObservedAt time.Time
}

const (
	Apple      Store       = "apple"
	Play       Store       = "play"
	Sandbox    Environment = "sandbox"
	Production Environment = "production"
	Monthly    Interval    = "monthly"
	Annual     Interval    = "annual"

	ReservationLifetime = 10 * time.Minute
)

var (
	ErrCheckoutBusy    = errors.New("a purchase is still pending")
	ErrAlreadyPremium  = errors.New("there is already an active subscription")
	ErrUnresolved      = errors.New("purchase outcome is unresolved")
	ErrPurchaseBinding = errors.New("verified purchase does not bind to the expected account and environment")
	ErrStalePurchase   = errors.New("older or conflicting verified purchase state")
)

// Attempt is server-owned. Binding is an RFC 4122 version 4 UUID that is
// opaque to the store. The account incarnation never goes to the provider.
type Attempt struct {
	ID, Binding string
	Store       Store
	Environment Environment
	Interval    Interval
}

// VerifiedPeriod must be constructed at the provider verification boundary.
// A client cannot provide or choose any of these fields directly.
type VerifiedPeriod struct {
	Store                              Store
	Environment                        Environment
	Binding, Subscription, Transaction string
	Interval                           Interval
	StartsAt, PaidThrough, ObservedAt  time.Time
	Revoked                            bool
	Renewal                            *RenewalObservation
}

type Journal struct {
	db         *sql.DB
	generation string
}

// NewJournal borrows the caller's private journal connection.
func NewJournal(db *sql.DB) *Journal { return &Journal{db: db} }

const schema = `
CREATE TABLE IF NOT EXISTS billing_attempts(
 id TEXT PRIMARY KEY NOT NULL, binding TEXT NOT NULL UNIQUE,
 family TEXT NOT NULL DEFAULT '',
 incarnation TEXT NOT NULL, environment TEXT NOT NULL CHECK(environment IN ('sandbox','production')),
 store TEXT NOT NULL CHECK(store IN ('apple','play')),
 interval TEXT NOT NULL CHECK(interval IN ('monthly','annual')),
 state TEXT NOT NULL CHECK(state IN ('reserved','launched','cancelled','fulfilled')),
 expires_ns INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX IF NOT EXISTS billing_one_checkout
 ON billing_attempts(incarnation,environment) WHERE state IN ('reserved','launched');
CREATE TABLE IF NOT EXISTS billing_subscriptions(
 environment TEXT NOT NULL, store TEXT NOT NULL, subscription TEXT NOT NULL,
 binding TEXT NOT NULL UNIQUE REFERENCES billing_attempts(binding),
 incarnation TEXT NOT NULL,
 PRIMARY KEY(environment,store,subscription)
) STRICT;
CREATE TABLE IF NOT EXISTS billing_periods(
 environment TEXT NOT NULL, store TEXT NOT NULL, proof TEXT NOT NULL,
 subscription TEXT NOT NULL, interval TEXT NOT NULL CHECK(interval IN ('monthly','annual')),
 starts_ns INTEGER NOT NULL, ends_ns INTEGER NOT NULL CHECK(ends_ns>starts_ns),
 observed_ns INTEGER NOT NULL, revoked INTEGER NOT NULL CHECK(revoked IN (0,1)),
 PRIMARY KEY(environment,store,proof),
 FOREIGN KEY(environment,store,subscription) REFERENCES billing_subscriptions(environment,store,subscription)
) STRICT;
CREATE TABLE IF NOT EXISTS billing_period_renewals(
 environment TEXT NOT NULL, store TEXT NOT NULL, proof TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('renews','ends')),
 observed_ns INTEGER NOT NULL, generation TEXT NOT NULL,
 PRIMARY KEY(environment,store,proof),
 FOREIGN KEY(environment,store,proof) REFERENCES billing_periods(environment,store,proof) ON DELETE CASCADE
) STRICT;
CREATE INDEX IF NOT EXISTS billing_periods_subscription ON billing_periods(environment,store,subscription,ends_ns);
CREATE INDEX IF NOT EXISTS billing_subscriptions_owner ON billing_subscriptions(incarnation,environment);
`

// Migrate is called explicitly against the separate, protected journal, never
// against the ordinary application database or at application startup.
func Migrate(ctx context.Context, db *sql.DB) error {
	return transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, schema+appleReconciliationSchema+applePollSchema+playInboxSchema+playPollSchema+playReplacementSchema+playSignupSchema+privacySchema+runtimeSchema); err != nil {
			return err
		}
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO billing_privacy_key(singleton,key) VALUES(1,?) ON CONFLICT DO NOTHING`, key[:])
		return err
	})
}

func transaction(ctx context.Context, db *sql.DB, f func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func binding() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:]), nil
}

func validOwner(id string) bool {
	data, err := hex.DecodeString(id)
	return err == nil && len(data) == 16 && hex.EncodeToString(data) == id
}
func validEnvironment(e Environment) bool { return e == Sandbox || e == Production }
func validOffer(s Store, e Environment, i Interval) bool {
	return validEnvironment(e) && (s == Apple || s == Play) && (i == Monthly || i == Annual)
}
func timeNS(t time.Time) int64 { return t.UnixNano() }

func (j *Journal) Reserve(ctx context.Context, owner string, env Environment, store Store, interval Interval, now time.Time) (Attempt, error) {
	return j.reserve(ctx, owner, "", env, store, interval, now)
}

func (j *Journal) ReserveNative(ctx context.Context, owner, family string, env Environment, store Store, interval Interval, now time.Time) (Attempt, error) {
	if !validOwner(family) {
		return Attempt{}, ErrPurchaseBinding
	}
	return j.reserve(ctx, owner, family, env, store, interval, now)
}

func (j *Journal) reserve(ctx context.Context, owner, family string, env Environment, store Store, interval Interval, now time.Time) (Attempt, error) {
	if !validOwner(owner) || !validOffer(store, env, interval) || now.IsZero() {
		return Attempt{}, ErrPurchaseBinding
	}
	id, err := binding()
	if err != nil {
		return Attempt{}, err
	}
	accountBinding, err := binding()
	if err != nil {
		return Attempt{}, err
	}
	attempt := Attempt{ID: id, Binding: accountBinding, Store: store, Environment: env, Interval: interval}
	err = transaction(ctx, j.db, func(tx *sql.Tx) error {
		// Acquire the write lock before any read. Expire only an attempt that
		// has never permitted a native sheet, including after a process restart.
		if _, err := tx.ExecContext(ctx, `UPDATE billing_attempts SET state='cancelled'
 WHERE incarnation=? AND environment=? AND state='reserved' AND expires_ns<=?`, owner, env, timeNS(now)); err != nil {
			return err
		}
		if err := j.requireRuntime(ctx, tx); err != nil {
			return err
		}
		if blocked, err := ownerFenced(ctx, tx, owner); err != nil {
			return err
		} else if blocked {
			return ErrPurchaseBinding
		}
		if pending, err := j.pendingRuntimeVerification(ctx, tx, owner, env); err != nil {
			return err
		} else if pending {
			return ErrUnresolved
		}
		if waiting, err := futurePaidPeriod(ctx, tx, owner, env, now); err != nil {
			return err
		} else if waiting {
			return ErrUnresolved
		}
		if active, err := activePeriod(ctx, tx, owner, env, now, ""); err != nil {
			return err
		} else if active != nil {
			return ErrAlreadyPremium
		}
		if family != "" {
			// Retry a reservation response that was lost before the native
			// sheet could open. Other installations cannot acquire it.
			prev := attempt
			err := tx.QueryRowContext(ctx, `SELECT id,binding FROM billing_attempts
WHERE incarnation=? AND environment=? AND store=? AND interval=? AND family=?
AND state='reserved' AND expires_ns>?`,
				owner, env, store, interval, family, timeNS(now)).Scan(&prev.ID, &prev.Binding)
			if err == nil {
				attempt = prev
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO billing_attempts(id,binding,incarnation,environment,store,interval,state,expires_ns,family)
 VALUES(?,?,?,?,?,?,'reserved',?,?) ON CONFLICT DO NOTHING`, id, accountBinding, owner, env, store, interval, timeNS(now.Add(ReservationLifetime)), family)
		if err != nil {
			return err
		}
		num, err := result.RowsAffected()
		if err == nil && num != 1 {
			return ErrCheckoutBusy
		}
		return err
	})
	if err != nil {
		return Attempt{}, err
	}
	return attempt, nil
}

// NativeAttempt resolves a server-owned handle only for the installation and
// verified account that reserved it. The store and environment are server-owned.
func (j *Journal) NativeAttempt(ctx context.Context, owner, family, id string, env Environment, store Store) (Attempt, error) {
	if !validOwner(owner) || !validOwner(family) || !validEnvironment(env) {
		return Attempt{}, ErrPurchaseBinding
	}
	if err := j.CurrentRuntime(ctx); err != nil {
		return Attempt{}, err
	}
	a := Attempt{ID: id, Store: store, Environment: env}
	err := j.db.QueryRowContext(ctx, `SELECT binding,interval FROM billing_attempts
WHERE id=? AND incarnation=? AND family=? AND environment=? AND store=? AND state<>'cancelled'
AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=billing_attempts.incarnation AND d.state!='cancelled')`,
		id, owner, family, env, store).Scan(&a.Binding, &a.Interval)
	if errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, ErrPurchaseBinding
	}
	return a, err
}

func (j *Journal) Launch(ctx context.Context, attempt Attempt, now time.Time) error {
	return transition(ctx, j.db, `UPDATE billing_attempts SET state='launched'
 WHERE id=? AND binding=? AND environment=? AND store=? AND interval=? AND state='reserved' AND expires_ns>?
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=billing_attempts.incarnation AND d.state!='cancelled')
 AND `+currentGenerationSQL+`=?`,
		attempt.ID, attempt.Binding, attempt.Environment, attempt.Store, attempt.Interval, timeNS(now), j.generation)
}

func (j *Journal) Cancel(ctx context.Context, attempt Attempt, _ time.Time, terminal bool) error {
	// A caller may cancel an unused, unlaunched reservation. Once launched,
	// only the provider or the authorized native flow can disambiguate it.
	return transition(ctx, j.db, `UPDATE billing_attempts SET state='cancelled'
 WHERE id=? AND binding=? AND environment=? AND store=? AND interval=?
 AND (state='reserved' OR (? AND state='launched')) AND `+currentGenerationSQL+`=?`,
		attempt.ID, attempt.Binding, attempt.Environment, attempt.Store, attempt.Interval, terminal, j.generation)
}

func transition(ctx context.Context, db *sql.DB, query string, args ...any) error {
	r, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n != 1 {
		return ErrUnresolved
	}
	return err
}

func (j *Journal) Fulfil(ctx context.Context, p VerifiedPeriod) error {
	return j.fulfil(ctx, p, "", false, nil)
}

type journalWriteCheck func(context.Context, *sql.Tx) error

func (j *Journal) fulfil(ctx context.Context, p VerifiedPeriod, expectedOwner string, existingOnly bool, beforeWrite journalWriteCheck) error {
	if !validOffer(p.Store, p.Environment, p.Interval) || p.Binding == "" || len(p.Subscription) == 0 || len(p.Subscription) > 256 ||
		len(p.Transaction) == 0 || len(p.Transaction) > 256 || p.StartsAt.IsZero() || !p.PaidThrough.After(p.StartsAt) || p.ObservedAt.IsZero() {
		return ErrPurchaseBinding
	}
	return transaction(ctx, j.db, func(tx *sql.Tx) error {
		// Lock before reading. The immutable original binding prevents a
		// restore from moving a provider subscription to another account.
		if _, err := tx.ExecContext(ctx, "UPDATE billing_attempts SET state=state WHERE binding=?", p.Binding); err != nil {
			return err
		}
		if err := j.requireRuntime(ctx, tx); err != nil {
			return err
		}
		if beforeWrite != nil {
			if err := beforeWrite(ctx, tx); err != nil {
				return err
			}
		}
		var owner, state string
		var original Interval
		err := tx.QueryRowContext(ctx, `SELECT incarnation,state,interval FROM billing_attempts WHERE binding=? AND environment=? AND store=?`,
			p.Binding, p.Environment, p.Store).Scan(&owner, &state, &original)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (state != "launched" && state != "fulfilled" || state == "launched" && original != p.Interval) {
			return ErrPurchaseBinding
		}
		if err != nil {
			return err
		}
		if expectedOwner != "" && owner != expectedOwner || existingOnly && state != "fulfilled" {
			return ErrPurchaseBinding
		}
		if blocked, err := ownerFenced(ctx, tx, owner); err != nil {
			return err
		} else if blocked {
			return ErrPurchaseBinding
		}
		if blocked, err := rejectedReplay(ctx, tx, p); err != nil {
			return err
		} else if blocked {
			return ErrPurchaseBinding
		}

		if !existingOnly {
			if _, err = tx.ExecContext(ctx, `INSERT INTO billing_subscriptions(environment,store,subscription,binding,incarnation)
 VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, p.Environment, p.Store, p.Subscription, p.Binding, owner); err != nil {
				return err
			}
		}
		var bound string
		err = tx.QueryRowContext(ctx, `SELECT binding FROM billing_subscriptions WHERE environment=? AND store=? AND subscription=?`,
			p.Environment, p.Store, p.Subscription).Scan(&bound)
		if errors.Is(err, sql.ErrNoRows) || err == nil && bound != p.Binding {
			return ErrPurchaseBinding
		}
		if err != nil {
			return err
		}

		var subscription string
		var interval Interval
		var start, end, observed int64
		var revoked bool
		err = tx.QueryRowContext(ctx, `SELECT subscription,interval,starts_ns,ends_ns,observed_ns,revoked FROM billing_periods
 WHERE environment=? AND store=? AND proof=?`, p.Environment, p.Store, p.Transaction).Scan(&subscription, &interval, &start, &end, &observed, &revoked)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if !p.Revoked {
				var overlap bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM billing_periods p
 JOIN billing_subscriptions s USING(environment,store,subscription)
 WHERE s.incarnation=? AND s.environment=? AND p.revoked=0 AND p.starts_ns<? AND p.ends_ns>?)`,
					owner, p.Environment, timeNS(p.PaidThrough), timeNS(p.StartsAt)).Scan(&overlap); err != nil {
					return err
				}
				if overlap {
					return ErrStalePurchase
				}
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO billing_periods(environment,store,proof,subscription,interval,starts_ns,ends_ns,observed_ns,revoked)
 VALUES(?,?,?,?,?,?,?,?,?)`, p.Environment, p.Store, p.Transaction, p.Subscription, p.Interval, timeNS(p.StartsAt), timeNS(p.PaidThrough), timeNS(p.ObservedAt), p.Revoked)
		case err != nil:
			return err
		default:
			if subscription != p.Subscription || interval != p.Interval || start != timeNS(p.StartsAt) || end != timeNS(p.PaidThrough) {
				return ErrPurchaseBinding
			}
			if timeNS(p.ObservedAt) < observed || revoked && !p.Revoked || timeNS(p.ObservedAt) == observed && revoked != p.Revoked {
				return ErrStalePurchase
			}
			_, err = tx.ExecContext(ctx, `UPDATE billing_periods SET observed_ns=?,revoked=? WHERE environment=? AND store=? AND proof=?`,
				timeNS(p.ObservedAt), p.Revoked, p.Environment, p.Store, p.Transaction)
		}
		if err != nil {
			return err
		}
		if err := j.recordRenewal(ctx, tx, p); err != nil {
			return err
		}
		if err := j.confirmRuntimeProof(ctx, tx, p); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE billing_attempts SET state='fulfilled' WHERE binding=? AND state='launched'", p.Binding)
		if err == nil && p.Store == Apple && appleTransactionID.MatchString(p.Subscription) {
			_, err = tx.ExecContext(ctx, `INSERT INTO billing_apple_polls(environment,binding,due_ns) VALUES(?,?,?)
 ON CONFLICT(environment,binding) DO NOTHING`, p.Environment, p.Binding, timeNS(p.ObservedAt.Add(applePollInterval)))
		}
		return err
	})
}

func (j *Journal) recordRenewal(ctx context.Context, tx *sql.Tx, p VerifiedPeriod) error {
	r := p.Renewal
	if r == nil || p.Revoked || (r.State != RenewalRenews && r.State != RenewalEnds) ||
		r.ObservedAt.Before(p.StartsAt) || !r.ObservedAt.Before(p.PaidThrough) {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO billing_period_renewals(environment,store,proof,state,observed_ns,generation)
 VALUES(?,?,?,?,?,?) ON CONFLICT(environment,store,proof) DO UPDATE SET
 state=excluded.state,observed_ns=excluded.observed_ns,generation=excluded.generation
 WHERE excluded.observed_ns>billing_period_renewals.observed_ns OR
 (excluded.observed_ns=billing_period_renewals.observed_ns AND excluded.state=billing_period_renewals.state)`,
		p.Environment, p.Store, p.Transaction, r.State, timeNS(r.ObservedAt), j.generation)
	return err
}

type reader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func activePeriod(ctx context.Context, db reader, owner string, env Environment, at time.Time, generation string) (*VerifiedPeriod, error) {
	p := VerifiedPeriod{Environment: env}
	var start, end, observed int64
	err := db.QueryRowContext(ctx, `SELECT s.store,s.binding,s.subscription,p.proof,p.interval,p.starts_ns,p.ends_ns,p.observed_ns
 FROM billing_periods p JOIN billing_subscriptions s USING(environment,store,subscription)
 WHERE s.incarnation=? AND s.environment=? AND p.revoked=0 AND p.starts_ns<=? AND p.ends_ns>?
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=s.incarnation AND d.state!='cancelled')
 AND (?='' OR EXISTS(SELECT 1 FROM billing_runtime_proofs f JOIN billing_runtime r USING(generation)
 WHERE f.environment=p.environment AND f.store=p.store AND f.proof=p.proof AND f.generation=?))
 ORDER BY p.ends_ns DESC, p.proof LIMIT 1`, owner, env, timeNS(at), timeNS(at), generation, generation).
		Scan(&p.Store, &p.Binding, &p.Subscription, &p.Transaction, &p.Interval, &start, &end, &observed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.StartsAt, p.PaidThrough, p.ObservedAt = time.Unix(0, start), time.Unix(0, end), time.Unix(0, observed)
	return &p, nil
}

func (j *Journal) Active(ctx context.Context, owner string, env Environment, at time.Time) (*VerifiedPeriod, error) {
	if !validOwner(owner) || !validEnvironment(env) || at.IsZero() {
		return nil, ErrPurchaseBinding
	}
	if err := j.CurrentRuntime(ctx); err != nil {
		return nil, err
	}
	return activePeriod(ctx, j.db, owner, env, at, j.generation)
}

// NativeOverview exposes only account-visible purchase facts. A resumable
// handle is installation-scoped and never includes the store binding or proof.
type NativeOverview struct {
	Pending      bool
	Subscription *SubscriptionSummary
	Management   *SubscriptionManagement
	Resume       *PendingAttempt
}

type SubscriptionSummary struct {
	Store       Store        `json:"store"`
	Interval    Interval     `json:"interval"`
	PaidThrough time.Time    `json:"paidThrough"`
	Renewal     RenewalState `json:"renewal,omitempty"`
}

// SubscriptionManagement describes a selling store without granting paid access.
// An old end date is absent until this runtime has verified the period.
type SubscriptionManagement struct {
	Store   Store      `json:"store"`
	State   string     `json:"state"`
	EndedAt *time.Time `json:"endedAt,omitempty"`
}

func futurePaidPeriod(ctx context.Context, db reader, owner string, env Environment, at time.Time) (bool, error) {
	var future bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM billing_periods p
 JOIN billing_subscriptions s USING(environment,store,subscription)
 WHERE s.incarnation=? AND s.environment=? AND p.revoked=0 AND p.starts_ns>?)`, owner, env, timeNS(at)).Scan(&future)
	return future, err
}

func (j *Journal) lastSubscriptionManagement(ctx context.Context, db reader, owner string, env Environment, at time.Time) (*SubscriptionManagement, error) {
	info := SubscriptionManagement{State: "pending_verification"}
	var end int64
	var revoked, verified bool
	err := db.QueryRowContext(ctx, `SELECT s.store,p.ends_ns,p.revoked,
 (?='' OR EXISTS(SELECT 1 FROM billing_runtime_proofs f
 WHERE f.environment=p.environment AND f.store=p.store AND f.proof=p.proof AND f.generation=?))
 FROM billing_periods p JOIN billing_subscriptions s USING(environment,store,subscription)
 WHERE s.incarnation=? AND s.environment=?
 ORDER BY p.starts_ns DESC,p.ends_ns DESC,p.observed_ns DESC,p.store,p.proof LIMIT 1`,
		j.generation, j.generation, owner, env).Scan(&info.Store, &end, &revoked, &verified)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if verified && revoked {
		info.State = "revoked"
	} else if verified && end <= timeNS(at) {
		info.State = "ended"
		endedAt := time.Unix(0, end).UTC()
		info.EndedAt = &endedAt
	}
	return &info, nil
}

type PendingAttempt struct {
	ID       string   `json:"id"`
	Interval Interval `json:"interval"`
	Launched bool     `json:"launched"`
}

func (j *Journal) NativeOverview(ctx context.Context, owner, family string, env Environment, store Store, now time.Time) (NativeOverview, error) {
	if !validOwner(owner) || !validOwner(family) || !validOffer(store, env, Monthly) || now.IsZero() {
		return NativeOverview{}, ErrPurchaseBinding
	}
	tx, err := j.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativeOverview{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := j.requireRuntime(ctx, tx); err != nil {
		return NativeOverview{}, err
	}
	if fenced, err := ownerFenced(ctx, tx, owner); err != nil {
		return NativeOverview{}, err
	} else if fenced {
		return NativeOverview{}, ErrPurchaseBinding
	}
	var result NativeOverview
	result.Pending, err = j.pendingRuntimeVerification(ctx, tx, owner, env)
	if err != nil {
		return NativeOverview{}, err
	}
	if !result.Pending {
		result.Pending, err = futurePaidPeriod(ctx, tx, owner, env, now)
		if err != nil {
			return NativeOverview{}, err
		}
	}
	active, err := activePeriod(ctx, tx, owner, env, now, j.generation)
	if err != nil {
		return NativeOverview{}, err
	}
	if active != nil {
		result.Subscription = &SubscriptionSummary{Store: active.Store, Interval: active.Interval, PaidThrough: active.PaidThrough.UTC()}
		err := tx.QueryRowContext(ctx, `SELECT state FROM billing_period_renewals
 WHERE environment=? AND store=? AND proof=? AND generation=? AND observed_ns<=?`,
			env, active.Store, active.Transaction, j.generation, timeNS(now)).Scan(&result.Subscription.Renewal)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return NativeOverview{}, err
		}
	}
	var resume PendingAttempt
	var savedStore Store
	var savedFamily, state string
	err = tx.QueryRowContext(ctx, `SELECT id,interval,store,family,state FROM billing_attempts
 WHERE incarnation=? AND environment=? AND (state='launched' OR (state='reserved' AND expires_ns>?))`,
		owner, env, timeNS(now)).Scan(&resume.ID, &resume.Interval, &savedStore, &savedFamily, &state)
	switch {
	case err == nil:
		result.Pending = true
		if active == nil && state == "launched" {
			result.Management = &SubscriptionManagement{Store: savedStore, State: "pending_verification"}
		}
		if family == savedFamily && store == savedStore {
			resume.Launched = state == "launched"
			result.Resume = &resume
		}
	case !errors.Is(err, sql.ErrNoRows):
		return NativeOverview{}, err
	}
	if active == nil && result.Management == nil {
		result.Management, err = j.lastSubscriptionManagement(ctx, tx, owner, env, now)
		if err != nil {
			return NativeOverview{}, err
		}
	}
	return result, tx.Commit()
}
