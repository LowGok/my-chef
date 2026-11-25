package billing

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	appleBatchSize = 25
	appleMaxTries  = 5
	appleLease     = time.Minute
	appleJobBudget = 20 * time.Second
)

const appleReconciliationSchema = `
CREATE TABLE IF NOT EXISTS billing_apple_notices(
 environment TEXT NOT NULL CHECK(environment IN ('sandbox','production')),
 event_id TEXT NOT NULL, binding TEXT NOT NULL REFERENCES billing_attempts(binding) ON DELETE CASCADE,
 transaction_id TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
 due_ns INTEGER NOT NULL, lease TEXT NOT NULL DEFAULT '', lease_ns INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL DEFAULT 'ready' CHECK(state IN ('ready','done','failed')),
 PRIMARY KEY(environment,event_id)
) STRICT;
CREATE INDEX IF NOT EXISTS billing_apple_notices_due ON billing_apple_notices(environment,state,due_ns,lease_ns);
CREATE INDEX IF NOT EXISTS billing_apple_notices_binding ON billing_apple_notices(binding);
CREATE TABLE IF NOT EXISTS billing_apple_history(
 environment TEXT NOT NULL CHECK(environment IN ('sandbox','production')), app TEXT NOT NULL,
 through_ms INTEGER NOT NULL, start_ms INTEGER NOT NULL DEFAULT 0, end_ms INTEGER NOT NULL DEFAULT 0,
 page TEXT NOT NULL DEFAULT '', PRIMARY KEY(environment,app)
) STRICT;
`

// AppleNotice contains only the routing fields from an authenticated Apple
// notification. The source must verify the Apple signature and environment
// before enqueueing. Enqueue never grants Premium, and stores no JWS or email.
type AppleNotice struct {
	Environment              Environment
	ID, Binding, Transaction string
}

func (j *Journal) EnqueueAppleNotice(ctx context.Context, n AppleNotice, now time.Time) error {
	err := j.retainAppleNotice(ctx, n, now)
	if errors.Is(err, errUnownedAppleNotice) {
		return ErrPurchaseBinding
	}
	return err
}

func (j *Journal) retainAppleNotice(ctx context.Context, n AppleNotice, now time.Time) error {
	if !validEnvironment(n.Environment) || !accountUUID.MatchString(n.ID) || !accountUUID.MatchString(n.Binding) ||
		!appleTransactionID.MatchString(n.Transaction) || now.IsZero() {
		return ErrPurchaseBinding
	}
	n.ID, n.Binding = strings.ToLower(n.ID), strings.ToLower(n.Binding)
	return transaction(ctx, j.db, func(tx *sql.Tx) error {
		if err := j.requireRuntime(ctx, tx); err != nil {
			return err
		}
		return enqueueAppleNotice(ctx, tx, n, now)
	})
}

var errUnownedAppleNotice = errors.New("notification has no eligible purchase binding")

func enqueueAppleNotice(ctx context.Context, tx *sql.Tx, n AppleNotice, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO billing_apple_notices(environment,event_id,binding,transaction_id,due_ns)
 SELECT environment,?,binding,?,? FROM billing_attempts
 WHERE binding=? AND store='apple' AND environment=? AND state IN ('launched','fulfilled')
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=billing_attempts.incarnation AND d.state!='cancelled')
 ON CONFLICT(environment,event_id) DO NOTHING`, n.ID, n.Transaction, timeNS(now), n.Binding, n.Environment)
	if err != nil {
		return err
	}
	var bound, ref string
	err = tx.QueryRowContext(ctx, `SELECT binding,transaction_id FROM billing_apple_notices WHERE environment=? AND event_id=?`,
		n.Environment, n.ID).Scan(&bound, &ref)
	if errors.Is(err, sql.ErrNoRows) {
		return errUnownedAppleNotice
	}
	if err == nil && (bound != n.Binding || ref != n.Transaction) {
		return ErrPurchaseBinding
	}
	return err
}

type appleWork struct {
	AppleNotice
	Lease    string
	Attempts int
}

func (j *Journal) claimAppleNotice(ctx context.Context, env Environment, now time.Time) (appleWork, error) {
	lease, err := binding()
	if err != nil {
		return appleWork{}, err
	}
	w := appleWork{AppleNotice: AppleNotice{Environment: env}, Lease: lease}
	err = j.db.QueryRowContext(ctx, `UPDATE billing_apple_notices SET lease=?, lease_ns=?
 WHERE environment=? AND event_id=(SELECT event_id FROM billing_apple_notices
 WHERE environment=? AND state='ready' AND due_ns<=? AND lease_ns<=? ORDER BY due_ns,event_id LIMIT 1)
 AND `+currentGenerationSQL+`=?
 RETURNING event_id,binding,transaction_id,attempts`, lease, timeNS(now.Add(appleLease)), env, env, timeNS(now), timeNS(now), j.generation).
		Scan(&w.ID, &w.Binding, &w.Transaction, &w.Attempts)
	return w, err
}

func (j *Journal) settleAppleNotice(ctx context.Context, w appleWork, now time.Time, success bool) error {
	state, attempts, due := "done", w.Attempts, now
	if !success {
		state, attempts = "ready", attempts+1
		due = now.Add(time.Minute << w.Attempts)
		if attempts >= appleMaxTries {
			state = "failed"
		}
	}
	// A crash, late reply, or deletion may remove this lease. Never change
	// another worker's lease or recreate work after the original owner is gone.
	_, err := j.db.ExecContext(ctx, `UPDATE billing_apple_notices SET state=?, attempts=?, due_ns=?, lease='',lease_ns=0
 WHERE environment=? AND event_id=? AND lease=? AND state='ready'`, state, attempts, timeNS(due), w.Environment, w.ID, w.Lease)
	return err
}

type ReconcileBatch struct {
	Processed int
	Failed    int // Total outstanding exhausted jobs, including jobs from earlier runs.
}

type AppleReconciler struct {
	Journal     *Journal
	Environment Environment
	Offers      []Offer
	Provider    RestoreProvider
	Now         func() time.Time
	// WithLiveOwner must confirm the original incarnation and deployment
	// generation and keep that authorization locked until the commit returns.
	WithLiveOwner func(context.Context, string, func() error) error
}

// RunBatch is one bounded drain; the application supplies scheduling and a
// health check for outstanding failures. Errors from the provider stay here.
func (r AppleReconciler) RunBatch(ctx context.Context) (ReconcileBatch, error) {
	var batch ReconcileBatch
	if r.Journal == nil || r.Provider == nil || r.WithLiveOwner == nil || !validEnvironment(r.Environment) {
		return batch, ErrStoreUnavailable
	}
	if err := r.Journal.CurrentRuntime(ctx); err != nil {
		return batch, err
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	for range appleBatchSize {
		w, err := r.Journal.claimAppleNotice(ctx, r.Environment, now())
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return batch, err
		}
		ok := r.reconcileNotice(ctx, w, now) == nil
		if err := r.Journal.settleAppleNotice(ctx, w, now(), ok); err != nil {
			return batch, err
		}
		batch.Processed++
	}
	err := r.Journal.db.QueryRowContext(ctx, `SELECT count(*) FROM billing_apple_notices WHERE environment=? AND state='failed'`,
		r.Environment).Scan(&batch.Failed)
	return batch, err
}

func (r AppleReconciler) reconcileNotice(parent context.Context, w appleWork, now func() time.Time) error {
	ctx, cancel := context.WithTimeout(parent, appleJobBudget)
	defer cancel()
	var owner string
	err := r.Journal.db.QueryRowContext(ctx, `SELECT incarnation FROM billing_attempts
 WHERE binding=? AND environment=? AND store='apple' AND state IN ('launched','fulfilled')`,
		w.Binding, w.Environment).Scan(&owner)
	if err != nil {
		return err
	}
	_, err = r.Journal.restoreWithLease(ctx, owner, Apple, w.Environment, r.Offers, w.Transaction,
		noticeVerifier{r.Provider, w}, func(commit func() error) error {
			return r.WithLiveOwner(ctx, owner, commit)
		}, func(ctx context.Context, tx *sql.Tx) error {
			var current bool
			err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM billing_apple_notices
 WHERE environment=? AND event_id=? AND binding=? AND lease=? AND lease_ns>? AND state='ready')`,
				w.Environment, w.ID, w.Binding, w.Lease, timeNS(now())).Scan(&current)
			if err != nil {
				return err
			}
			if !current {
				return ErrUnresolved
			}
			return nil
		})
	return err
}

type noticeVerifier struct {
	RestoreProvider
	work appleWork
}

func (v noticeVerifier) Verify(ctx context.Context, id string) (ProviderProof, error) {
	return v.bound(v.RestoreProvider.Verify(ctx, id))
}

func (v noticeVerifier) VerifyExisting(ctx context.Context, id string) (ProviderProof, error) {
	return v.bound(v.RestoreProvider.VerifyExisting(ctx, id))
}

func (v noticeVerifier) bound(proof ProviderProof, err error) (ProviderProof, error) {
	if err == nil && (proof.Period.Binding != v.work.Binding || proof.Period.Transaction != v.work.Transaction) {
		return ProviderProof{}, ErrPurchaseBinding
	}
	return proof, err
}
