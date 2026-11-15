package billing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"time"
)

const nativeCancellationSchema = `
CREATE TABLE IF NOT EXISTS billing_cancel_challenges(
 attempt TEXT PRIMARY KEY NOT NULL REFERENCES billing_attempts(id) ON DELETE CASCADE,
 digest BLOB NOT NULL CHECK(length(digest)=32), issued_ns INTEGER NOT NULL,
 expires_ns INTEGER NOT NULL CHECK(expires_ns>issued_ns), generation TEXT NOT NULL
) STRICT;
`

// NativeCancellation binds one server-authorized store launch to its session.
// The challenge by itself does not authorize a cancellation.
type NativeCancellation struct {
	Attempt     Attempt
	Owner       string
	Family      string
	AppInstance [32]byte
	Challenge   [32]byte
}

// NativeCancellationVerifier is a server-configured trust boundary. It must
// verify an assertion from the exact native app and installation for the
// original, still-running SDK call. A client claim is not an assertion.
type NativeCancellationVerifier interface {
	VerifyCancellation(context.Context, NativeCancellation, []byte) error
}

// LaunchForNativeCancellation takes an instance identifier established by the
// server's app attestation verifier, never an identifier claimed by the app.
func (j *Journal) LaunchForNativeCancellation(ctx context.Context, a Attempt, owner, family string, appInstance [32]byte, now time.Time) (NativeCancellation, error) {
	if !validOwner(owner) || !validOwner(family) || appInstance == [32]byte{} || !validOffer(a.Store, a.Environment, a.Interval) || now.IsZero() {
		return NativeCancellation{}, ErrPurchaseBinding
	}
	c := NativeCancellation{Attempt: a, Owner: owner, Family: family, AppInstance: appInstance}
	if _, err := rand.Read(c.Challenge[:]); err != nil {
		return NativeCancellation{}, err
	}
	digest := nativeCancellationDigest(c)
	err := transaction(ctx, j.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE billing_attempts SET state='launched'
 WHERE id=? AND binding=? AND environment=? AND store=? AND interval=? AND incarnation=? AND family=?
 AND state='reserved' AND expires_ns>? AND `+currentGenerationSQL+`=?
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=billing_attempts.incarnation AND d.state!='cancelled')`,
			a.ID, a.Binding, a.Environment, a.Store, a.Interval, owner, family, timeNS(now), j.generation)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrUnresolved
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO billing_cancel_challenges(attempt,digest,issued_ns,expires_ns,generation)
 VALUES(?,?,?,?,?)`, a.ID, digest[:], timeNS(now), timeNS(now.Add(ReservationLifetime)), j.generation)
		return err
	})
	if err != nil {
		return NativeCancellation{}, err
	}
	return c, nil
}

func nativeCancellationDigest(c NativeCancellation) [sha256.Size]byte {
	var binding [64]byte
	copy(binding[:32], c.Challenge[:])
	copy(binding[32:], c.AppInstance[:])
	return sha256.Sum256(binding[:])
}

const nativeCancellationEligible = `
 WHERE id=? AND binding=? AND environment=? AND store=? AND interval=? AND incarnation=? AND family=?
 AND state='launched' AND ` + currentGenerationSQL + `=?
 AND EXISTS(SELECT 1 FROM billing_cancel_challenges c WHERE c.attempt=billing_attempts.id
 AND c.generation=? AND c.digest=? AND c.issued_ns<=? AND c.expires_ns>?)
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=billing_attempts.incarnation AND d.state!='cancelled')
 AND NOT EXISTS(SELECT 1 FROM billing_subscriptions s WHERE s.binding=billing_attempts.binding)
 AND NOT EXISTS(SELECT 1 FROM billing_apple_notices n WHERE n.binding=billing_attempts.binding)
 AND NOT EXISTS(SELECT 1 FROM billing_play_notices n WHERE n.binding=billing_attempts.binding)
 AND NOT EXISTS(SELECT 1 FROM billing_play_polls n WHERE n.binding=billing_attempts.binding)
 AND NOT EXISTS(SELECT 1 FROM billing_play_signups s WHERE s.binding=billing_attempts.binding)`

// CompleteNativeCancellation never calls the verifier while holding a SQLite
// write transaction. The commit repeats all checks and consumes the challenge
// together with releasing the original launch, even if a store event arrived
// or the process crossed an expiration boundary during verification.
func (j *Journal) CompleteNativeCancellation(ctx context.Context, c NativeCancellation, proof []byte, v NativeCancellationVerifier,
	withLiveSession func(func() error) error, clock func() time.Time) error {
	a := c.Attempt
	if v == nil || withLiveSession == nil || !validOwner(c.Owner) || !validOwner(c.Family) || !validOffer(a.Store, a.Environment, a.Interval) ||
		(c.Challenge == [32]byte{} || c.AppInstance == [32]byte{}) || len(proof) == 0 || len(proof) > 16<<10 {
		return ErrPurchaseBinding
	}
	if clock == nil {
		clock = time.Now
	}
	digest := nativeCancellationDigest(c)
	args := func(at time.Time) []any {
		return []any{a.ID, a.Binding, a.Environment, a.Store, a.Interval, c.Owner, c.Family, j.generation,
			j.generation, digest[:], timeNS(at), timeNS(at)}
	}
	var eligible bool
	if err := j.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM billing_attempts`+nativeCancellationEligible+`)`, args(clock())...).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return ErrUnresolved
	}
	if err := v.VerifyCancellation(ctx, c, proof); err != nil {
		return ErrPurchaseBinding
	}
	// The caller must recheck the original authenticated session while
	// holding its account lock around this write, after provider I/O.
	return withLiveSession(func() error {
		return transaction(ctx, j.db, func(tx *sql.Tx) error {
			result, err := tx.ExecContext(ctx, `UPDATE billing_attempts SET state='cancelled'`+nativeCancellationEligible, args(clock())...)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return ErrUnresolved
			}
			_, err = tx.ExecContext(ctx, "DELETE FROM billing_cancel_challenges WHERE attempt=?", a.ID)
			return err
		})
	})
}
