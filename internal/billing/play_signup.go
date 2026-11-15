package billing

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

const playSignupSchema = `
CREATE TABLE IF NOT EXISTS billing_play_signups(
 binding TEXT NOT NULL REFERENCES billing_attempts(binding) ON DELETE CASCADE,
 token_digest TEXT NOT NULL CHECK(length(token_digest)=64),
 PRIMARY KEY(binding,token_digest)
) STRICT;
`

// playSignup is authenticated purchase state, without evidence of a paid
// period. It intentionally cannot be used as a ProviderProof.
type playSignup struct {
	offer           Offer
	binding, digest string
	cancelled       bool
}

func (*playSignup) Error() string { return ErrUnresolved.Error() }
func (*playSignup) Unwrap() error { return ErrUnresolved }

func (p *PlayProvider) signupState(sub playSubscription, token string) error {
	if sub.Region != "BE" || sub.Linked != "" || sub.Start != "" || len(sub.Lines) != 1 {
		return ErrUnresolved
	}
	line := sub.Lines[0]
	if line.Product != p.cfg.Product || line.Order != "" || line.Expiry != "" ||
		line.Auto == nil || line.Auto.Installments != nil || line.Prepaid != nil ||
		line.Deferred != nil || line.Removed != nil || line.Replacement != nil ||
		line.Promotion != nil || line.Offer.OfferID != "" {
		return ErrUnresolved
	}
	var interval Interval
	switch line.Offer.BasePlan {
	case p.cfg.MonthlyBasePlan:
		interval = Monthly
	case p.cfg.AnnualBasePlan:
		interval = Annual
	default:
		return ErrPurchaseBinding
	}
	return &playSignup{
		offer: Offer{Store: Play, Environment: p.cfg.Environment, App: p.cfg.Package, Product: line.Product,
			BasePlan: line.Offer.BasePlan, Interval: interval},
		binding: strings.ToLower(sub.Account.Binding), digest: playTokenDigest(token),
		cancelled: sub.State == "SUBSCRIPTION_STATE_PENDING_PURCHASE_CANCELED",
	}
}

func (i *PlayInbox) retainSignup(ctx context.Context, p *playSignup, token, expectedOwner string) error {
	if p == nil || p.offer.Store != Play || p.offer.Environment != i.env || !i.Offers(p.offer) ||
		!accountUUID.MatchString(p.binding) || p.digest != playTokenDigest(token) {
		return ErrPurchaseBinding
	}
	return transaction(ctx, i.journal.db, func(tx *sql.Tx) error {
		// Serialize cancellation with fulfilment, and recheck the generation
		// and deletion fence after provider I/O.
		if _, err := tx.ExecContext(ctx, "UPDATE billing_attempts SET state=state WHERE binding=?", p.binding); err != nil {
			return err
		}
		if err := i.journal.requireRuntime(ctx, tx); err != nil {
			return err
		}
		var owner, state string
		if err := tx.QueryRowContext(ctx, `SELECT incarnation,state FROM billing_attempts
 WHERE binding=? AND environment=? AND store='play' AND interval=?`, p.binding, i.env, p.offer.Interval).Scan(&owner, &state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrPurchaseBinding
			}
			return err
		}
		if expectedOwner != "" && expectedOwner != owner {
			return ErrPurchaseBinding
		}
		if blocked, err := ownerFenced(ctx, tx, owner); err != nil {
			return err
		} else if blocked {
			return ErrPurchaseBinding
		}
		if !p.cancelled {
			if state != "launched" {
				return ErrPurchaseBinding
			}
			// Two distinct verified tokens already establish ambiguity. Bound
			// storage while retaining enough evidence to prevent unsafe release.
			_, err := tx.ExecContext(ctx, `INSERT INTO billing_play_signups(binding,token_digest)
 SELECT ?,? WHERE (SELECT count(*) FROM billing_play_signups WHERE binding=?)<2
 ON CONFLICT DO NOTHING`, p.binding, p.digest, p.binding)
			return err
		}
		var exact, conflicting bool
		if err := tx.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM billing_play_signups WHERE binding=? AND token_digest=?),
 EXISTS(SELECT 1 FROM billing_play_signups WHERE binding=? AND token_digest<>?) OR
 EXISTS(SELECT 1 FROM billing_subscriptions WHERE binding=?) OR
 EXISTS(SELECT 1 FROM billing_play_notices WHERE binding=? AND state IN ('ready','failed'))`,
			p.binding, p.digest, p.binding, p.digest, p.binding, p.binding).Scan(&exact, &conflicting); err != nil {
			return err
		}
		if !exact || conflicting {
			return ErrUnresolved
		}
		if state == "cancelled" {
			return nil // The same verified terminal fact may be redelivered.
		}
		if state != "launched" {
			return ErrUnresolved
		}
		_, err := tx.ExecContext(ctx, `UPDATE billing_attempts SET state='cancelled' WHERE binding=? AND state='launched'`, p.binding)
		return err
	})
}
