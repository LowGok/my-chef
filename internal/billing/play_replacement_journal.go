package billing

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const playReplacementSchema = `
CREATE TABLE IF NOT EXISTS billing_play_replacements(
 environment TEXT NOT NULL CHECK(environment IN ('sandbox','production')),
 binding TEXT NOT NULL REFERENCES billing_attempts(binding) ON DELETE CASCADE,
 predecessor TEXT NOT NULL CHECK(length(predecessor)=64),
 successor TEXT NOT NULL CHECK(length(successor)=64 AND successor!=predecessor),
 previous_interval TEXT NOT NULL CHECK(previous_interval IN ('monthly','annual')),
 next_interval TEXT NOT NULL CHECK(next_interval IN ('monthly','annual') AND next_interval!=previous_interval),
 effective_ns INTEGER NOT NULL,
 ciphertext BLOB NOT NULL CHECK(length(ciphertext) BETWEEN 29 AND 2076),
 PRIMARY KEY(environment,predecessor), UNIQUE(environment,successor)
) STRICT;
CREATE INDEX IF NOT EXISTS billing_play_replacements_binding ON billing_play_replacements(binding);
`

type playReplacementVerifier interface {
	VerifyReplacement(context.Context, string) (PlayReplacement, error)
}

type linkedPlayPeriodVerifier interface {
	VerifyLinkedPeriod(context.Context, string, string) (ProviderProof, error)
}

// StageReplacement records a next-billing-date transition for its original
// owner and switches the polling reference in the same transaction. The
// existing paid period stays unchanged until the successor's order is funded.
func (i *PlayInbox) StageReplacement(ctx context.Context, owner, token string, fence func(func() error) error) error {
	if !validOwner(owner) {
		return ErrPurchaseBinding
	}
	return i.retainReplacement(ctx, owner, token, fence, nil)
}

func (i *PlayInbox) retainReplacement(ctx context.Context, owner, token string, fence func(func() error) error,
	record func(context.Context, *sql.Tx, PlayReplacement) error) error {
	provider, ok := i.provider.(playReplacementVerifier)
	if !ok || !validPlayToken(token) {
		return ErrPurchaseBinding
	}
	if err := i.journal.CurrentRuntime(ctx); err != nil {
		return err
	}
	replacement, err := provider.VerifyReplacement(ctx, token)
	if err != nil {
		return err
	}
	if !i.replacementConfigured(replacement) ||
		replacement.Current != playTokenDigest(token) || replacement.Current == replacement.Previous ||
		len(replacement.Previous) != 64 || !accountUUID.MatchString(replacement.Binding) ||
		replacement.ObservedAt.IsZero() || replacement.StartedAt.IsZero() || !replacement.StoreExpiry.After(replacement.StartedAt) {
		return ErrPurchaseBinding
	}
	late, err := i.verifyLateReplacement(ctx, owner, token, replacement)
	if err != nil {
		return err
	}
	commit := func() error {
		return transaction(ctx, i.journal.db, func(tx *sql.Tx) error {
			who := owner
			if who == "" {
				// Only the authenticated notice path lacks a request
				// account. Look up the original owner by the store binding.
				err := tx.QueryRowContext(ctx, `SELECT incarnation FROM billing_subscriptions
 WHERE environment=? AND store='play' AND binding=?`, i.env, replacement.Binding).Scan(&who)
				if errors.Is(err, sql.ErrNoRows) {
					return ErrPurchaseBinding
				}
				if err != nil {
					return err
				}
			}
			if err := i.stageReplacement(ctx, tx, who, token, replacement, late); err != nil {
				return err
			}
			if record != nil {
				return record(ctx, tx, replacement)
			}
			return nil
		})
	}
	if fence != nil {
		return fence(commit)
	}
	return commit()
}

func (i *PlayInbox) replacementConfigured(r PlayReplacement) bool {
	if r.Environment != i.env || !playRegion.MatchString(r.Region) ||
		!validOffer(Play, i.env, r.PreviousInterval) || !validOffer(Play, i.env, r.CurrentInterval) ||
		r.PreviousInterval == r.CurrentInterval {
		return false
	}
	var before, after bool
	for _, offer := range i.offers {
		if offer.Store != Play || offer.Environment != i.env || offer.App != r.StoreApp || offer.Product != r.Product {
			continue
		}
		before = before || offer.Interval == r.PreviousInterval
		after = after || offer.Interval == r.CurrentInterval
	}
	return before && after
}

// An expiry beyond the predecessor's paid boundary cannot itself establish
// a late transition. Obtain its new paid order without holding a write lock;
// the old boundary and owner are checked again when recording the transition.
func (i *PlayInbox) verifyLateReplacement(ctx context.Context, owner, token string, r PlayReplacement) (*VerifiedPeriod, error) {
	cutoff, err := i.predecessorCutoff(ctx, i.journal.db, owner, r)
	if errors.Is(err, sql.ErrNoRows) {
		// An already-staged successor no longer has the old poll reference.
		return nil, nil
	}
	if err != nil || timeNS(r.StoreExpiry) == cutoff {
		return nil, err
	}
	if timeNS(r.ObservedAt) < cutoff || timeNS(r.StoreExpiry) < cutoff {
		return nil, ErrPurchaseBinding
	}
	store, ok := i.provider.(linkedPlayPeriodVerifier)
	if !ok {
		return nil, ErrPurchaseBinding
	}
	proof, err := store.VerifyLinkedPeriod(ctx, token, r.Previous)
	if errors.Is(err, ErrUnresolved) {
		return nil, ErrPurchaseBinding
	}
	if err != nil {
		return nil, err
	}
	p := proof.Period
	if !latePaidBoundary(&p, r, cutoff) || proof.App != r.StoreApp || proof.Product != r.Product {
		return nil, ErrPurchaseBinding
	}
	for _, offer := range i.offers {
		if offer.matches(proof) {
			return &p, nil
		}
	}
	return nil, ErrPurchaseBinding
}

func latePaidBoundary(p *VerifiedPeriod, r PlayReplacement, cutoff int64) bool {
	return p != nil && !p.Revoked && p.Store == Play && p.Environment == r.Environment &&
		p.Binding == r.Binding && p.Subscription == r.Current && p.Interval == r.CurrentInterval &&
		playOrderID.MatchString(p.Transaction) && p.StartsAt.Equal(time.Unix(0, cutoff)) &&
		!p.ObservedAt.Before(p.StartsAt) && p.PaidThrough.After(r.ObservedAt) &&
		!p.PaidThrough.After(r.StoreExpiry)
}

func (i *PlayInbox) predecessorCutoff(ctx context.Context, db reader, owner string, r PlayReplacement) (int64, error) {
	var cutoff int64
	err := db.QueryRowContext(ctx, `SELECT p.ends_ns FROM billing_subscriptions s
 JOIN billing_periods p USING(environment,store,subscription)
 JOIN billing_play_polls q ON q.environment=s.environment AND q.binding=s.binding AND q.token_digest=?
 WHERE s.environment=? AND s.store='play' AND s.binding=? AND (?='' OR s.incarnation=?)
 AND p.interval=? AND p.starts_ns<=? AND p.ends_ns>? AND p.revoked=0
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=s.incarnation AND d.state!='cancelled')
 ORDER BY p.ends_ns DESC LIMIT 1`, r.Previous, i.env, r.Binding, owner, owner, r.PreviousInterval,
		timeNS(r.StartedAt), timeNS(r.StartedAt)).Scan(&cutoff)
	return cutoff, err
}

func (i *PlayInbox) stageReplacement(ctx context.Context, tx *sql.Tx, owner, token string, r PlayReplacement, late *VerifiedPeriod) error {
	if err := i.journal.requireRuntime(ctx, tx); err != nil {
		return err
	}
	var existingBinding, existingSuccessor string
	var from, to Interval
	var effective int64
	err := tx.QueryRowContext(ctx, `SELECT r.binding,r.successor,r.previous_interval,r.next_interval,r.effective_ns
 FROM billing_play_replacements r
 JOIN billing_subscriptions s ON s.environment=r.environment AND s.store='play' AND s.binding=r.binding
 JOIN billing_play_polls q ON q.environment=s.environment AND q.binding=s.binding AND q.token_digest=r.successor
 WHERE r.environment=? AND r.predecessor=? AND s.incarnation=?
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=s.incarnation AND d.state!='cancelled')`,
		i.env, r.Previous, owner).Scan(&existingBinding, &existingSuccessor, &from, &to, &effective)
	if err == nil {
		if existingBinding != r.Binding || existingSuccessor != r.Current || from != r.PreviousInterval ||
			to != r.CurrentInterval || timeNS(r.StoreExpiry) < effective || timeNS(r.StartedAt) >= effective {
			return ErrUnresolved
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	cutoff, err := i.predecessorCutoff(ctx, tx, owner, r)
	if errors.Is(err, sql.ErrNoRows) || err == nil && cutoff != timeNS(r.StoreExpiry) && !latePaidBoundary(late, r, cutoff) {
		return ErrPurchaseBinding
	}
	if err != nil {
		return err
	}
	raw := []byte(token)
	defer clear(raw)
	stageContext := []byte("mychef/play/replacement/v1\x00" + string(i.env) + "\x00" + r.Binding + "\x00" + r.Previous + "\x00" + r.Current)
	ciphertext := i.seal.Seal(nil, nil, raw, stageContext)
	res, err := tx.ExecContext(ctx, `INSERT INTO billing_play_replacements(environment,binding,predecessor,successor,previous_interval,next_interval,effective_ns,ciphertext)
 VALUES(?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, i.env, r.Binding, r.Previous, r.Current, r.PreviousInterval, r.CurrentInterval, cutoff, ciphertext)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrUnresolved
	}
	encryptedPoll := i.seal.Seal(nil, nil, raw, i.pollContext(r.Binding, r.Current))
	res, err = tx.ExecContext(ctx, `UPDATE billing_play_polls SET token_digest=?,ciphertext=?,due_ns=?,failures=0,lease='',lease_ns=0
 WHERE environment=? AND binding=? AND token_digest=?`, r.Current, encryptedPoll, timeNS(r.ObservedAt), i.env, r.Binding, r.Previous)
	if err != nil {
		return err
	}
	rows, err = res.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrUnresolved
	}
	return nil
}

// refreshCarryover can re-confirm an existing paid proof after a restart.
// The linked subscription must still cover that exact period. It writes only
// the runtime verification marker, so it cannot extend or revive a period.
func (r PlayReconciler) refreshCarryover(ctx context.Context, token, owner, canonical, predecessor string,
	expected Interval, cutoff time.Time, work playPoll, now func() time.Time) error {
	i := r.Inbox
	store, ok := i.provider.(playReplacementVerifier)
	if !ok {
		return ErrStoreUnavailable
	}
	fresh, err := store.VerifyReplacement(ctx, token)
	if err != nil {
		return err
	}
	if !i.replacementConfigured(fresh) || fresh.Binding != work.binding || fresh.Previous != predecessor ||
		fresh.Current != work.digest || fresh.CurrentInterval != expected || !fresh.StoreExpiry.Equal(cutoff) ||
		fresh.StartedAt.IsZero() || fresh.ObservedAt.IsZero() || fresh.ObservedAt.Before(fresh.StartedAt) ||
		!fresh.ObservedAt.Before(cutoff) {
		return ErrUnresolved
	}
	return r.WithLiveOwner(ctx, owner, func() error {
		return transaction(ctx, i.journal.db, func(tx *sql.Tx) error {
			checked := now()
			if !checked.Before(cutoff) {
				return ErrUnresolved
			}
			if err := i.pollLease(ctx, tx, work, owner, checked); err != nil {
				return err
			}
			paid := VerifiedPeriod{Environment: i.env, Store: Play}
			err := tx.QueryRowContext(ctx, `SELECT p.proof FROM billing_periods p
 JOIN billing_subscriptions s USING(environment,store,subscription)
 JOIN billing_play_replacements r ON r.environment=s.environment AND r.binding=s.binding
 WHERE p.environment=? AND p.store='play' AND p.subscription=? AND s.incarnation=?
 AND r.predecessor=? AND r.successor=? AND r.previous_interval=? AND r.next_interval=?
 AND r.effective_ns=p.ends_ns AND p.interval=r.previous_interval
 AND p.revoked=0 AND p.starts_ns<=? AND p.ends_ns=?`, i.env, canonical, owner, predecessor, work.digest,
				fresh.PreviousInterval, expected, timeNS(fresh.StartedAt), timeNS(cutoff)).Scan(&paid.Transaction)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrUnresolved
			}
			if err != nil {
				return err
			}
			return i.journal.confirmRuntimeProof(ctx, tx, paid)
		})
	})
}
