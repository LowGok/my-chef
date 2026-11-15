package billing

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"time"
)

const applePollInterval = 15 * time.Minute

const applePollSchema = `
CREATE TABLE IF NOT EXISTS billing_apple_polls(
 environment TEXT NOT NULL CHECK(environment IN ('sandbox','production')),
 binding TEXT NOT NULL REFERENCES billing_subscriptions(binding) ON DELETE CASCADE,
 due_ns INTEGER NOT NULL, lease TEXT NOT NULL DEFAULT '', lease_ns INTEGER NOT NULL DEFAULT 0,
 failures INTEGER NOT NULL DEFAULT 0 CHECK(failures BETWEEN 0 AND 5),
 PRIMARY KEY(environment,binding)
) STRICT;
CREATE INDEX IF NOT EXISTS billing_apple_polls_due ON billing_apple_polls(environment,due_ns,lease_ns);
INSERT INTO billing_apple_polls(environment,binding,due_ns)
 SELECT environment,binding,0 FROM billing_subscriptions WHERE store='apple'
 ON CONFLICT(environment,binding) DO NOTHING;
`

type appleStatusProvider interface {
	currentApplePeriod(context.Context, string, string) (ProviderProof, error)
}

func (p *AppleProvider) currentApplePeriod(ctx context.Context, lineage, account string) (ProviderProof, error) {
	if !appleTransactionID.MatchString(lineage) || !accountUUID.MatchString(account) ||
		p.cfg.Environment == Production && p.cfg.AppAppleID <= 0 {
		return ProviderProof{}, ErrPurchaseBinding
	}
	at := p.now()
	token, err := p.bearer(at)
	if err != nil {
		return ProviderProof{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.origin+"/inApps/v1/subscriptions/"+lineage, nil)
	if err != nil {
		return ProviderProof{}, ErrStoreUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	res, err := p.http.Do(req)
	if err != nil {
		return ProviderProof{}, ErrStoreUnavailable
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return ProviderProof{}, ErrStoreUnavailable
	}
	const limit = 512 << 10
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	var response struct {
		AppAppleID  int64  `json:"appAppleId"`
		BundleID    string `json:"bundleId"`
		Environment string `json:"environment"`
		Groups      []struct {
			Group  string `json:"subscriptionGroupIdentifier"`
			Latest []struct {
				Original    string `json:"originalTransactionId"`
				Status      int    `json:"status"`
				Transaction string `json:"signedTransactionInfo"`
			} `json:"lastTransactions"`
		} `json:"data"`
	}
	if err != nil || len(raw) > limit || json.Unmarshal(raw, &response) != nil {
		return ProviderProof{}, ErrStoreUnavailable
	}
	env := "Production"
	if p.cfg.Environment == Sandbox {
		env = "Sandbox"
	}
	if response.BundleID != p.cfg.BundleID || response.Environment != env ||
		(p.cfg.Environment == Production && response.AppAppleID != p.cfg.AppAppleID) {
		return ProviderProof{}, ErrPurchaseBinding
	}
	var proof ProviderProof
	for _, group := range response.Groups {
		if group.Group != p.cfg.GroupID {
			continue
		}
		for _, last := range group.Latest {
			if last.Original != lineage {
				continue
			}
			if proof.Period.Transaction != "" || last.Status < 1 || last.Status > 5 {
				return ProviderProof{}, ErrPurchaseBinding
			}
			tx, err := p.verifyTransaction(last.Transaction, at)
			if err != nil {
				return ProviderProof{}, err
			}
			proof, err = p.normalizeTransaction(tx, false, at)
			if err != nil {
				return ProviderProof{}, err
			}
			if proof.Period.Subscription != lineage || proof.Period.Binding != account ||
				(last.Status == 5) != proof.Period.Revoked {
				return ProviderProof{}, ErrPurchaseBinding
			}
			// An expired subscription, billing retry, or grace period cannot
			// be used to project access beyond a verified paid-through date.
			if last.Status >= 2 && last.Status <= 4 && proof.Period.PaidThrough.After(at) {
				return ProviderProof{}, ErrStoreUnavailable
			}
		}
	}
	if proof.Period.Transaction == "" {
		return ProviderProof{}, ErrStoreUnavailable
	}
	return proof, nil
}

type applePoll struct {
	Environment    Environment
	Binding, Lease string
	Failures       int
}

// PollKnown refreshes existing Apple subscriptions when callbacks are missing.
// It only applies a current verified period to the original owner. A failed
// poll leaves the current paid-through boundary unchanged.
func (r AppleReconciler) PollKnown(ctx context.Context) (ReconcileBatch, error) {
	var batch ReconcileBatch
	provider, ok := r.Provider.(appleStatusProvider)
	if r.Journal == nil || !ok || r.WithLiveOwner == nil || !validEnvironment(r.Environment) {
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
		w, err := r.Journal.claimApplePoll(ctx, r.Environment, now())
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return batch, err
		}
		ok := r.refreshApplePoll(ctx, provider, w, now) == nil
		if err := r.Journal.settleApplePoll(ctx, w, now(), ok); err != nil {
			return batch, err
		}
		batch.Processed++
	}
	err := r.Journal.db.QueryRowContext(ctx, `SELECT count(*) FROM billing_apple_polls WHERE environment=? AND failures=?`,
		r.Environment, appleMaxTries).Scan(&batch.Failed)
	return batch, err
}

func (j *Journal) claimApplePoll(ctx context.Context, env Environment, at time.Time) (applePoll, error) {
	lease, err := binding()
	if err != nil {
		return applePoll{}, err
	}
	w := applePoll{Environment: env, Lease: lease}
	err = j.db.QueryRowContext(ctx, `UPDATE billing_apple_polls SET lease=?,lease_ns=?
 WHERE environment=? AND binding=(SELECT binding FROM billing_apple_polls
 WHERE environment=? AND due_ns<=? AND lease_ns<=? ORDER BY due_ns,binding LIMIT 1)
 AND `+currentGenerationSQL+`=?
 RETURNING binding,failures`, lease, timeNS(at.Add(appleLease)), env, env, timeNS(at), timeNS(at), j.generation).Scan(&w.Binding, &w.Failures)
	return w, err
}

func (j *Journal) settleApplePoll(ctx context.Context, w applePoll, at time.Time, ok bool) error {
	failures, delay := 0, applePollInterval
	if !ok {
		failures = min(w.Failures+1, appleMaxTries)
		delay = min(time.Minute<<w.Failures, applePollInterval)
	}
	_, err := j.db.ExecContext(ctx, `UPDATE billing_apple_polls SET failures=?,due_ns=?,lease='',lease_ns=0
 WHERE environment=? AND binding=? AND lease=?`, failures, timeNS(at.Add(delay)), w.Environment, w.Binding, w.Lease)
	return err
}

func (r AppleReconciler) refreshApplePoll(ctx context.Context, provider appleStatusProvider, w applePoll, now func() time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, appleJobBudget)
	defer cancel()
	var owner, lineage string
	if err := r.Journal.db.QueryRowContext(ctx, `SELECT incarnation,subscription FROM billing_subscriptions
 WHERE environment=? AND store='apple' AND binding=?
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=billing_subscriptions.incarnation AND d.state!='cancelled')`,
		w.Environment, w.Binding).Scan(&owner, &lineage); err != nil {
		return err
	}
	proof, err := provider.currentApplePeriod(ctx, lineage, w.Binding)
	if err != nil {
		return err
	}
	p := proof.Period
	if p.Store != Apple || p.Environment != w.Environment || p.Subscription != lineage || p.Binding != w.Binding {
		return ErrPurchaseBinding
	}
	for _, offer := range r.Offers {
		if !offer.matches(proof) {
			continue
		}
		return r.WithLiveOwner(ctx, owner, func() error {
			return r.Journal.fulfil(ctx, p, owner, true, func(ctx context.Context, tx *sql.Tx) error {
				var current bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM billing_apple_polls
 WHERE environment=? AND binding=? AND lease=? AND lease_ns>?)`, w.Environment, w.Binding, w.Lease, timeNS(now())).Scan(&current); err != nil {
					return err
				}
				if !current {
					return ErrUnresolved
				}
				return nil
			})
		})
	}
	return ErrPurchaseBinding
}
