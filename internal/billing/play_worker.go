package billing

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	playWorkBatch  = 10
	playMaxTries   = 5
	playWorkLease  = time.Minute
	playWorkBudget = 45 * time.Second
)

type playWork struct {
	id, binding, digest, lease string
	ciphertext                 []byte
	attempts                   int
}

func (i *PlayInbox) claim(ctx context.Context, now time.Time) (playWork, error) {
	lease, err := binding()
	if err != nil {
		return playWork{}, err
	}
	work := playWork{lease: lease}
	err = i.journal.db.QueryRowContext(ctx, `UPDATE billing_play_notices SET lease=?,lease_ns=?
 WHERE environment=? AND event_id=(SELECT n.event_id FROM billing_play_notices n
 JOIN billing_attempts a USING(binding)
 WHERE n.environment=? AND n.state='ready' AND n.due_ns<=? AND n.lease_ns<=?
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=a.incarnation AND d.state!='cancelled')
 ORDER BY n.due_ns,n.event_id LIMIT 1)
 AND `+currentGenerationSQL+`=?
 RETURNING event_id,binding,token_digest,ciphertext,attempts`, lease, timeNS(now.Add(playWorkLease)),
		i.env, i.env, timeNS(now), timeNS(now), i.journal.generation).
		Scan(&work.id, &work.binding, &work.digest, &work.ciphertext, &work.attempts)
	return work, err
}

func (i *PlayInbox) settle(ctx context.Context, work playWork, now time.Time, success bool) error {
	state, attempts, due := "done", work.attempts, now
	if !success {
		state, attempts = "ready", attempts+1
		due = now.Add(time.Minute << min(work.attempts, playMaxTries))
		if attempts >= playMaxTries {
			state = "failed"
		}
	}
	_, err := i.journal.db.ExecContext(ctx, `UPDATE billing_play_notices SET state=?,attempts=?,due_ns=?,lease='',lease_ns=0
 WHERE environment=? AND event_id=? AND state='ready' AND lease=? AND lease_ns>?
 AND `+currentGenerationSQL+`=?`,
		state, attempts, timeNS(due), i.env, work.id, work.lease, timeNS(now), i.journal.generation)
	return err
}

type PlayReconciler struct {
	Inbox *PlayInbox
	// Acknowledge must use the same configured Play package and environment as
	// the inbox verifier. It is invoked only following the durable grant.
	Acknowledge            func(context.Context, string, string) error
	AcknowledgeReplacement func(context.Context, string, string, string) error
	// WithLiveOwner checks the original incarnation and locks that authority
	// until the local commit or readiness read returns.
	WithLiveOwner func(context.Context, string, func() error) error
	Now           func() time.Time
}

func (r PlayReconciler) RunBatch(ctx context.Context) (ReconcileBatch, error) {
	var batch ReconcileBatch
	if r.Inbox == nil || r.Acknowledge == nil || r.WithLiveOwner == nil {
		return batch, ErrStoreUnavailable
	}
	i := r.Inbox
	if err := i.journal.CurrentRuntime(ctx); err != nil {
		return batch, err
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	for range playWorkBatch {
		work, err := i.claim(ctx, now())
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return batch, err
		}
		ok := r.reconcile(ctx, work, now) == nil
		if err := i.settle(ctx, work, now(), ok); err != nil {
			return batch, err
		}
		batch.Processed++
	}
	err := i.journal.db.QueryRowContext(ctx, `SELECT count(*) FROM billing_play_notices
 WHERE environment=? AND state='failed'`, i.env).Scan(&batch.Failed)
	return batch, err
}

// leaseCurrent is checked inside the entitlement transaction and again just
// before acknowledgement; no database locks span a provider request.
func (i *PlayInbox) leaseCurrent(ctx context.Context, db reader, work playWork, now time.Time) error {
	var eligible bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM billing_play_notices n JOIN billing_attempts a USING(binding)
 WHERE n.environment=? AND n.event_id=? AND n.binding=? AND n.token_digest=? AND n.lease=?
 AND n.lease_ns>? AND n.state='ready' AND a.state IN ('launched','fulfilled')
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=a.incarnation AND d.state!='cancelled')
 AND `+currentGenerationSQL+`=?)`, i.env, work.id, work.binding, work.digest, work.lease, timeNS(now), i.journal.generation).Scan(&eligible)
	if err != nil {
		return err
	}
	if !eligible {
		return ErrUnresolved
	}
	return nil
}

func (r PlayReconciler) reconcile(parent context.Context, work playWork, now func() time.Time) error {
	ctx, cancel := context.WithTimeout(parent, playWorkBudget)
	defer cancel()
	i := r.Inbox
	if err := i.leaseCurrent(ctx, i.journal.db, work, now()); err != nil {
		return err
	}
	token, err := i.open(work.id, work.binding, work.digest, work.ciphertext)
	if err != nil {
		return err
	}
	var owner string
	err = i.journal.db.QueryRowContext(ctx, `SELECT incarnation FROM billing_attempts
 WHERE binding=? AND environment=? AND store='play' AND state IN ('launched','fulfilled')`, work.binding, i.env).Scan(&owner)
	if err != nil {
		return err
	}
	result, err := i.journal.restoreWithHooks(ctx, owner, Play, i.env, i.offers, token,
		playNoticeVerifier{i.provider, work}, func(commit func() error) error {
			return r.WithLiveOwner(ctx, owner, commit)
		}, func(ctx context.Context, tx *sql.Tx) error {
			return i.leaseCurrent(ctx, tx, work, now())
		}, i.remember(token))
	if err != nil {
		return err
	}
	if !result.CompleteStoreTransaction {
		return ErrUnresolved
	}
	if err := r.WithLiveOwner(ctx, owner, func() error {
		return i.leaseCurrent(ctx, i.journal.db, work, now())
	}); err != nil {
		return err
	}
	return r.Acknowledge(ctx, token, work.binding)
}

type playNoticeVerifier struct {
	RestoreProvider
	work playWork
}

func (v playNoticeVerifier) Verify(ctx context.Context, token string) (ProviderProof, error) {
	return v.bound(v.RestoreProvider.Verify(ctx, token))
}

func (v playNoticeVerifier) VerifyExisting(ctx context.Context, token string) (ProviderProof, error) {
	return v.bound(v.RestoreProvider.VerifyExisting(ctx, token))
}

func (v playNoticeVerifier) bound(proof ProviderProof, err error) (ProviderProof, error) {
	if err == nil && (proof.Period.Binding != v.work.binding || proof.Period.Subscription != v.work.digest) {
		return ProviderProof{}, ErrPurchaseBinding
	}
	return proof, err
}
