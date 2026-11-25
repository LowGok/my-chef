package billing

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const playPollInterval = 15 * time.Minute

const playPollSchema = `
CREATE TABLE IF NOT EXISTS billing_play_polls(
 environment TEXT NOT NULL CHECK(environment IN ('sandbox','production')),
 binding TEXT NOT NULL REFERENCES billing_attempts(binding) ON DELETE CASCADE,
 token_digest TEXT NOT NULL CHECK(length(token_digest)=64),
 ciphertext BLOB NOT NULL CHECK(length(ciphertext) BETWEEN 29 AND 2076),
 due_ns INTEGER NOT NULL, failures INTEGER NOT NULL DEFAULT 0,
 lease TEXT NOT NULL DEFAULT '', lease_ns INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(environment,binding)
) STRICT;
CREATE INDEX IF NOT EXISTS billing_play_polls_due ON billing_play_polls(environment,due_ns,lease_ns);
`

// UsesJournal keeps the route and worker on the same protected generation.
func (i *PlayInbox) UsesJournal(j *Journal, env Environment) bool {
	return i != nil && i.journal == j && i.env == env
}

// Offers reports whether this exact Play plan can be reconciled and polled.
func (i *PlayInbox) Offers(offer Offer) bool {
	for _, candidate := range i.offers {
		if candidate == offer {
			return true
		}
	}
	return false
}

// ReconcileCheckout commits the encrypted polling reference with the grant.
func (i *PlayInbox) ReconcileCheckout(ctx context.Context, owner string, attempt Attempt, offer Offer, token string, fence func(func() error) error) (CheckoutResult, error) {
	if attempt.Store != Play || attempt.Environment != i.env || !validPlayToken(token) {
		return CheckoutResult{}, ErrPurchaseBinding
	}
	if !i.Offers(offer) {
		return CheckoutResult{}, ErrPurchaseBinding
	}
	result, err := i.journal.reconcileCheckoutWithHook(ctx, owner, attempt, offer, token, i.provider, fence, i.remember(token))
	var pending *playSignup
	if !errors.As(err, &pending) {
		return result, err
	}
	if pending.binding != attempt.Binding || pending.offer != offer {
		return CheckoutResult{}, ErrPurchaseBinding
	}
	return i.reconcileSignup(ctx, pending, token, owner, fence)
}

// Restore applies the same transaction rule to a recovered purchase.
func (i *PlayInbox) Restore(ctx context.Context, owner string, token string, fence func(func() error) error) (CheckoutResult, error) {
	if !validPlayToken(token) {
		return CheckoutResult{}, ErrPurchaseBinding
	}
	result, err := i.journal.restoreWithHooks(ctx, owner, Play, i.env, i.offers, token, i.provider, fence, nil, i.remember(token))
	var signup *playSignup
	if errors.As(err, &signup) {
		return i.reconcileSignup(ctx, signup, token, owner, fence)
	}
	if errors.Is(err, ErrUnresolved) {
		// Retain a linked purchase without telling the device it can finish
		// an unresolved period. The Play worker owns acknowledgement.
		if stageErr := i.StageReplacement(ctx, owner, token, fence); stageErr != nil &&
			!errors.Is(stageErr, ErrPurchaseBinding) && !errors.Is(stageErr, ErrUnresolved) {
			return CheckoutResult{}, stageErr
		}
	}
	return result, err
}

func (i *PlayInbox) pollContext(binding, digest string) []byte {
	return []byte("mychef/play/poll/v1\x00" + string(i.env) + "\x00" + binding + "\x00" + digest)
}

func (i *PlayInbox) remember(token string) journalPeriodHook {
	return func(ctx context.Context, tx *sql.Tx, p VerifiedPeriod) error {
		if !validPlayToken(token) || p.Store != Play || p.Environment != i.env || !accountUUID.MatchString(p.Binding) ||
			p.Subscription != playTokenDigest(token) {
			return ErrPurchaseBinding
		}
		return i.savePoll(ctx, tx, p.Binding, p.Subscription, token, p.ObservedAt)
	}
}

func (i *PlayInbox) savePoll(ctx context.Context, tx *sql.Tx, binding, digest, token string, observed time.Time) error {
	// Keep the token in the independent encrypted journal. A failed write
	// also rolls back purchase state captured by the calling transaction.
	raw := []byte(token)
	encrypted := i.seal.Seal(nil, nil, raw, i.pollContext(binding, digest))
	clear(raw)
	res, err := tx.ExecContext(ctx, `INSERT INTO billing_play_polls(environment,binding,token_digest,ciphertext,due_ns)
 VALUES(?,?,?,?,?) ON CONFLICT(environment,binding) DO NOTHING`,
		i.env, binding, digest, encrypted, timeNS(observed.Add(playPollInterval)))
	if err != nil {
		return err
	}
	written, err := res.RowsAffected()
	if err != nil || written == 1 {
		return err
	}
	var savedDigest string
	var saved []byte
	if err := tx.QueryRowContext(ctx, `SELECT token_digest,ciphertext FROM billing_play_polls
 WHERE environment=? AND binding=?`, i.env, binding).Scan(&savedDigest, &saved); err != nil {
		return err
	}
	if savedDigest != digest {
		return ErrPurchaseBinding
	}
	_, err = i.openPoll(playPoll{binding: binding, digest: digest, ciphertext: saved})
	return err
}

type playPoll struct {
	binding, digest, lease string
	ciphertext             []byte
	failures               int
}

func (i *PlayInbox) openPoll(w playPoll) (string, error) {
	raw, err := i.seal.Open(nil, nil, w.ciphertext, i.pollContext(w.binding, w.digest))
	if err != nil {
		return "", ErrPurchaseBinding
	}
	defer clear(raw)
	token := string(raw)
	if !validPlayToken(token) || playTokenDigest(token) != w.digest {
		return "", ErrPurchaseBinding
	}
	return token, nil
}

func (i *PlayInbox) claimPoll(ctx context.Context, now time.Time) (playPoll, error) {
	lease, err := binding()
	if err != nil {
		return playPoll{}, err
	}
	w := playPoll{lease: lease}
	err = i.journal.db.QueryRowContext(ctx, `UPDATE billing_play_polls SET lease=?,lease_ns=?
 WHERE environment=? AND binding=(SELECT p.binding FROM billing_play_polls p
 JOIN billing_attempts a ON a.binding=p.binding AND a.environment=p.environment AND a.store='play' AND a.state IN ('launched','fulfilled')
 WHERE p.environment=? AND p.due_ns<=? AND p.lease_ns<=?
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=a.incarnation AND d.state!='cancelled')
 ORDER BY p.due_ns,p.binding LIMIT 1)
 AND `+currentGenerationSQL+`=?
 RETURNING binding,token_digest,ciphertext,failures`, lease, timeNS(now.Add(playWorkLease)), i.env, i.env, timeNS(now), timeNS(now), i.journal.generation).
		Scan(&w.binding, &w.digest, &w.ciphertext, &w.failures)
	return w, err
}

func (i *PlayInbox) settlePoll(ctx context.Context, w playPoll, now time.Time, ok bool) error {
	failures, delay := 0, playPollInterval
	if !ok {
		failures = min(w.failures+1, playMaxTries)
		delay = min(time.Minute<<min(w.failures, playMaxTries), playPollInterval)
	}
	_, err := i.journal.db.ExecContext(ctx, `UPDATE billing_play_polls SET failures=?,due_ns=?,lease='',lease_ns=0
 WHERE environment=? AND binding=? AND lease=? AND lease_ns>? AND `+currentGenerationSQL+`=?`,
		failures, timeNS(now.Add(delay)), i.env, w.binding, w.lease, timeNS(now), i.journal.generation)
	return err
}

// PollKnown refreshes known Play purchases even when a notification is lost.
func (r PlayReconciler) PollKnown(ctx context.Context) (ReconcileBatch, error) {
	var batch ReconcileBatch
	i := r.Inbox
	if i == nil || r.WithLiveOwner == nil || r.Acknowledge == nil {
		return batch, ErrStoreUnavailable
	}
	if err := i.journal.CurrentRuntime(ctx); err != nil {
		return batch, err
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	for range playWorkBatch {
		work, err := i.claimPoll(ctx, now())
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return batch, err
		}
		ok := r.refreshKnown(ctx, work, now) == nil
		if err := i.settlePoll(ctx, work, now(), ok); err != nil {
			return batch, err
		}
		batch.Processed++
	}
	err := i.journal.db.QueryRowContext(ctx, `SELECT count(*) FROM billing_play_polls WHERE environment=? AND failures=?`,
		i.env, playMaxTries).Scan(&batch.Failed)
	return batch, err
}

func (i *PlayInbox) pollLease(ctx context.Context, db reader, w playPoll, owner string, now time.Time) error {
	var eligible bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM billing_play_polls p
 JOIN billing_attempts a ON a.binding=p.binding AND a.environment=p.environment AND a.store='play' AND a.state IN ('launched','fulfilled')
 WHERE p.environment=? AND p.binding=? AND p.token_digest=? AND p.lease=? AND p.lease_ns>? AND a.incarnation=?
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=a.incarnation AND d.state!='cancelled')
 AND `+currentGenerationSQL+`=?)`, i.env, w.binding, w.digest, w.lease, timeNS(now), owner, i.journal.generation).Scan(&eligible)
	if err == nil && !eligible {
		return ErrUnresolved
	}
	return err
}

func (r PlayReconciler) refreshKnown(parent context.Context, w playPoll, now func() time.Time) error {
	ctx, cancel := context.WithTimeout(parent, playWorkBudget)
	defer cancel()
	i := r.Inbox
	var owner, state string
	var interval Interval
	if err := i.journal.db.QueryRowContext(ctx, `SELECT incarnation,state,interval FROM billing_attempts
 WHERE binding=? AND environment=? AND store='play'`, w.binding, i.env).Scan(&owner, &state, &interval); err != nil {
		return err
	}
	if state == "launched" {
		return r.refreshSignup(ctx, w, owner, interval, now)
	}
	var canonical, predecessor string
	var expectedInterval Interval
	var boundary int64
	if err := i.journal.db.QueryRowContext(ctx, `SELECT s.incarnation,s.subscription,
 COALESCE(r.predecessor,''),COALESCE(r.next_interval,''),COALESCE(r.effective_ns,0)
 FROM billing_subscriptions s LEFT JOIN billing_play_replacements r
 ON r.environment=s.environment AND r.binding=s.binding AND r.successor=?
 WHERE s.environment=? AND s.store='play' AND s.binding=? AND (s.subscription=? OR r.successor=?)`,
		w.digest, i.env, w.binding, w.digest, w.digest).Scan(&owner, &canonical, &predecessor, &expectedInterval, &boundary); err != nil {
		return err
	}
	if err := i.pollLease(ctx, i.journal.db, w, owner, now()); err != nil {
		return err
	}
	token, err := i.openPoll(w)
	if err != nil {
		return err
	}
	acknowledge := func() error {
		if err := r.WithLiveOwner(ctx, owner, func() error {
			return i.pollLease(ctx, i.journal.db, w, owner, now())
		}); err != nil {
			return err
		}
		if predecessor != "" {
			if r.AcknowledgeReplacement == nil {
				return ErrStoreUnavailable
			}
			return r.AcknowledgeReplacement(ctx, token, w.binding, predecessor)
		}
		return r.Acknowledge(ctx, token, w.binding)
	}
	effective := time.Unix(0, boundary)
	if predecessor != "" && now().Before(effective) {
		// The store requires acknowledgement of the registered successor
		// before its first renewal. Only the existing paid period is carried.
		if err := r.refreshCarryover(ctx, token, owner, canonical, predecessor, expectedInterval, effective, w, now); err != nil {
			return err
		}
		return acknowledge()
	}
	var proof ProviderProof
	if predecessor == "" {
		proof, err = i.provider.VerifyExisting(ctx, token)
	} else {
		store, ok := i.provider.(linkedPlayPeriodVerifier)
		if !ok {
			return ErrStoreUnavailable
		}
		proof, err = store.VerifyLinkedPeriod(ctx, token, predecessor)
	}
	if err != nil {
		return err
	}
	p := proof.Period
	if p.Store != Play || p.Environment != i.env || p.Binding != w.binding || p.Subscription != w.digest ||
		predecessor != "" && (p.Interval != expectedInterval || p.StartsAt.Before(effective)) {
		return ErrPurchaseBinding
	}
	for _, offer := range i.offers {
		if !offer.matches(proof) {
			continue
		}
		p.Subscription = canonical
		err := r.WithLiveOwner(ctx, owner, func() error {
			return i.journal.fulfil(ctx, p, owner, true, func(ctx context.Context, tx *sql.Tx) error {
				return i.pollLease(ctx, tx, w, owner, now())
			})
		})
		if err != nil || p.Revoked || !p.PaidThrough.After(now()) {
			return err
		}
		return acknowledge()
	}
	return ErrPurchaseBinding
}

func (r PlayReconciler) refreshSignup(ctx context.Context, w playPoll, owner string, interval Interval, now func() time.Time) error {
	i := r.Inbox
	if err := i.pollLease(ctx, i.journal.db, w, owner, now()); err != nil {
		return err
	}
	token, err := i.openPoll(w)
	if err != nil {
		return err
	}
	proof, err := i.provider.Verify(ctx, token)
	check := func(ctx context.Context, tx *sql.Tx) error { return i.pollLease(ctx, tx, w, owner, now()) }
	var p *playSignup
	if errors.As(err, &p) {
		if p.binding != w.binding || p.digest != w.digest || p.offer.Interval != interval || !i.Offers(p.offer) {
			return ErrPurchaseBinding
		}
		if !p.cancelled {
			return nil // A still-pending result refreshes the normal retry schedule.
		}
		return r.WithLiveOwner(ctx, owner, func() error { return i.retainSignup(ctx, p, token, owner, check) })
	}
	if err != nil {
		return err
	}
	attempt := Attempt{Binding: w.binding, Store: Play, Environment: i.env, Interval: interval}
	if proof.Period.Subscription != w.digest {
		return ErrPurchaseBinding
	}
	for _, offer := range i.offers {
		if !offer.admits(attempt, proof) {
			continue
		}
		if err := r.WithLiveOwner(ctx, owner, func() error {
			return i.journal.fulfil(ctx, proof.Period, owner, false, check)
		}); err != nil {
			return err
		}
		if err := r.WithLiveOwner(ctx, owner, func() error {
			return i.pollLease(ctx, i.journal.db, w, owner, now())
		}); err != nil {
			return err
		}
		return r.Acknowledge(ctx, token, w.binding)
	}
	return ErrPurchaseBinding
}
