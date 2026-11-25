package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"time"

	"github.com/LowGok/my-chef/internal/billing"
)

func (j *LifecycleJournal) BillingDeletionStores(ctx context.Context, user int64, env billing.Environment, now time.Time) ([]billing.Store, error) {
	if err := j.CheckAvailable(ctx); err != nil {
		return nil, err
	}
	var inc string
	if err := j.app.SQL.QueryRowContext(ctx, `SELECT incarnation FROM account_incarnations WHERE user_id=?`, user).Scan(&inc); err != nil {
		return nil, ErrAccountAccess
	}
	rows, err := j.store.SQL.QueryContext(ctx, `SELECT DISTINCT a.store FROM billing_attempts a
 WHERE a.incarnation=? AND a.environment=? AND (a.state='launched' OR (a.state='fulfilled' AND EXISTS(
 SELECT 1 FROM billing_subscriptions s WHERE s.binding=a.binding AND (
 EXISTS(SELECT 1 FROM billing_periods p WHERE p.environment=s.environment AND p.store=s.store
 AND p.subscription=s.subscription AND p.revoked=0 AND p.ends_ns>?)
 OR NOT EXISTS(SELECT 1 FROM billing_periods p JOIN billing_runtime_proofs f USING(environment,store,proof)
 JOIN billing_runtime r USING(generation) WHERE p.environment=s.environment AND p.store=s.store AND p.subscription=s.subscription)
 )))) ORDER BY a.store`, inc, env, now.UnixNano())
	if err != nil {
		return nil, ErrLifecycleJournal
	}
	defer func() { _ = rows.Close() }()
	stores := []billing.Store{}
	for rows.Next() {
		var seller billing.Store
		if err := rows.Scan(&seller); err != nil {
			return nil, ErrLifecycleJournal
		}
		stores = append(stores, seller)
	}
	if rows.Err() != nil {
		return nil, ErrLifecycleJournal
	}
	return stores, nil
}

func writeBillingExport(ctx context.Context, m exportMapper, w io.Writer, opts AccountExportOptions) error {
	if opts.billingRead == nil {
		_, err := io.WriteString(w, "[]\n")
		return err
	}
	rows, err := opts.billingRead.QueryContext(ctx, `SELECT p.store,p.proof,p.interval,p.starts_ns,p.ends_ns,
 CASE WHEN p.revoked=1 THEN 'revoked' WHEN p.ends_ns<=? THEN 'ended'
 WHEN NOT EXISTS(SELECT 1 FROM billing_runtime_proofs f JOIN billing_runtime r USING(generation)
 WHERE f.environment=p.environment AND f.store=p.store AND f.proof=p.proof) THEN 'pending_verification'
 WHEN p.starts_ns>? THEN 'scheduled' ELSE 'active' END AS state, 'period' AS kind
 FROM billing_periods p JOIN billing_subscriptions s USING(environment,store,subscription)
 WHERE s.incarnation=? AND p.environment=?
 UNION ALL SELECT store,id,interval,NULL,NULL,'pending_verification','attempt' FROM billing_attempts
 WHERE incarnation=? AND environment=? AND state='launched'
 ORDER BY 4,1,2`, opts.now.UnixNano(), opts.now.UnixNano(), opts.billingOwner, opts.BillingEnvironment, opts.billingOwner, opts.BillingEnvironment)
	if err != nil {
		return ErrAccountExport
	}
	defer func() { _ = rows.Close() }()
	if _, err := io.WriteString(w, "[\n"); err != nil {
		return err
	}
	first := true
	enc := json.NewEncoder(w)
	for rows.Next() {
		var store, ref, interval, status, kind string
		var begins, ends sql.NullInt64
		if err := rows.Scan(&store, &ref, &interval, &begins, &ends, &status, &kind); err != nil {
			return ErrAccountExport
		}
		record := map[string]string{"id": m.id("billing-"+kind, store+":"+ref), "store": store, "interval": interval, "status": status}
		if begins.Valid {
			record["startsAt"] = time.Unix(0, begins.Int64).UTC().Format(time.RFC3339Nano)
		}
		if ends.Valid {
			record["paidThrough"] = time.Unix(0, ends.Int64).UTC().Format(time.RFC3339Nano)
		}
		if !first {
			if _, err := io.WriteString(w, ",\n"); err != nil {
				return err
			}
		}
		first = false
		if err := enc.Encode(record); err != nil {
			return err
		}
	}
	if rows.Err() != nil {
		return ErrAccountExport
	}
	_, err = io.WriteString(w, "]\n")
	return err
}
