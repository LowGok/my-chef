package billing

import (
	"context"
	"database/sql"
)

const runtimeSchema = `
CREATE TABLE IF NOT EXISTS billing_runtime(
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), generation TEXT NOT NULL UNIQUE CHECK(length(generation)=36)
) STRICT;
CREATE TABLE IF NOT EXISTS billing_runtime_proofs(
 environment TEXT NOT NULL, store TEXT NOT NULL, proof TEXT NOT NULL, generation TEXT NOT NULL,
 PRIMARY KEY(environment,store,proof),
 FOREIGN KEY(environment,store,proof) REFERENCES billing_periods(environment,store,proof) ON DELETE CASCADE
) STRICT;
`

// StartRuntime is called under the app and protected journal locks. Its
// generation is never copied into an app snapshot. Previously verified sale
// records stay intact, but a new runtime requires a fresh provider lookup.
func StartRuntime(ctx context.Context, db *sql.DB) (*Journal, error) {
	if err := CheckStorage(ctx, db); err != nil {
		return nil, err
	}
	generation, err := binding()
	if err != nil {
		return nil, err
	}
	err = transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO billing_runtime(singleton,generation) VALUES(1,?)
 ON CONFLICT(singleton) DO UPDATE SET generation=excluded.generation`, generation); err != nil {
			return err
		}
		// Remove old leases and make the known purchases immediately due.
		// Pending notices keep their references and backoff across restarts.
		for _, query := range []string{
			`DELETE FROM billing_runtime_proofs`,
			`DELETE FROM billing_cancel_challenges`,
			`DELETE FROM billing_apple_enrollments`,
			`UPDATE billing_play_polls SET lease='',lease_ns=0,due_ns=0`,
			`UPDATE billing_play_notices SET lease='',lease_ns=0 WHERE state='ready'`,
			`UPDATE billing_apple_notices SET lease='',lease_ns=0 WHERE state='ready'`,
			`UPDATE billing_apple_polls SET lease='',lease_ns=0,due_ns=0`,
		} {
			if _, err := tx.ExecContext(ctx, query); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Journal{db: db, generation: generation}, nil
}

const currentGenerationSQL = `COALESCE((SELECT generation FROM billing_runtime WHERE singleton=1),'')`

func (j *Journal) requireRuntime(ctx context.Context, db reader) error {
	var saved string
	if err := db.QueryRowContext(ctx, "SELECT "+currentGenerationSQL).Scan(&saved); err != nil {
		return err
	}
	if saved != j.generation {
		return ErrUnresolved
	}
	return nil
}

// CurrentRuntime fences a worker or HTTP operation that was prepared before a
// restore. The journal repeats the check under its payment write transaction.
func (j *Journal) CurrentRuntime(ctx context.Context) error {
	return j.requireRuntime(ctx, j.db)
}

func (j *Journal) pendingRuntimeVerification(ctx context.Context, db reader, owner string, env Environment) (bool, error) {
	if j.generation == "" {
		return false, nil
	}
	var pending bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM billing_subscriptions s WHERE s.incarnation=? AND s.environment=?
 AND NOT EXISTS(SELECT 1 FROM billing_periods p JOIN billing_runtime_proofs f USING(environment,store,proof)
 WHERE p.environment=s.environment AND p.store=s.store AND p.subscription=s.subscription AND f.generation=?))`,
		owner, env, j.generation).Scan(&pending)
	return pending, err
}

func (j *Journal) confirmRuntimeProof(ctx context.Context, tx *sql.Tx, p VerifiedPeriod) error {
	if j.generation == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO billing_runtime_proofs(environment,store,proof,generation) VALUES(?,?,?,?)
 ON CONFLICT(environment,store,proof) DO UPDATE SET generation=excluded.generation`,
		p.Environment, p.Store, p.Transaction, j.generation)
	return err
}
