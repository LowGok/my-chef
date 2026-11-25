package billing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const appleEnrollmentLifetime = 5 * time.Minute

const appleAttestRegistrySchema = `
CREATE TABLE IF NOT EXISTS billing_apple_enrollments(
 incarnation TEXT NOT NULL, family TEXT NOT NULL, environment TEXT NOT NULL,
 scope BLOB NOT NULL CHECK(length(scope)=32), digest BLOB NOT NULL CHECK(length(digest)=32),
 issued_ns INTEGER NOT NULL, expires_ns INTEGER NOT NULL CHECK(expires_ns>issued_ns), generation TEXT NOT NULL,
 PRIMARY KEY(incarnation,family,environment)
) STRICT;
CREATE INDEX IF NOT EXISTS billing_apple_enrollments_expiry ON billing_apple_enrollments(expires_ns);
CREATE TABLE IF NOT EXISTS billing_apple_attested_keys(
 incarnation TEXT NOT NULL, family TEXT NOT NULL, environment TEXT NOT NULL,
 scope BLOB NOT NULL CHECK(length(scope)=32), key_id BLOB NOT NULL UNIQUE CHECK(length(key_id)=32),
 point BLOB NOT NULL CHECK(length(point)=65),
 counter INTEGER NOT NULL CHECK(counter BETWEEN 0 AND 4294967295),
 PRIMARY KEY(incarnation,family,environment)
) STRICT;
`

// AppleCancellationRegistry binds a verified iOS key to one native session
// family. The approved app, trust roots and build policy are server inputs.
type AppleCancellationRegistry struct {
	Journal     *Journal
	Environment Environment
	Verifier    AppleEnrollmentVerifier
}

func (r AppleCancellationRegistry) scope(owner, family string) ([32]byte, error) {
	p := r.Verifier.Build
	if r.Journal == nil || !validEnvironment(r.Environment) || !validOwner(owner) || !validOwner(family) ||
		r.Verifier.AppID == "" || len(r.Verifier.AppID) > 255 || r.Verifier.Roots == nil ||
		p.Category < 2 || p.Category > 5 || p.Version == "" || len(p.Version) > 64 {
		return [32]byte{}, ErrPurchaseBinding
	}
	input := fmt.Sprintf("mychef.apple.attest.policy.v1\x00%s\x00%t\x00%t\x00%d\x00%s",
		r.Verifier.AppID, r.Verifier.Development, p.AllowLegacy, p.Category, p.Version)
	return sha256.Sum256([]byte(input)), nil
}

func (r AppleCancellationRegistry) BeginEnrollment(ctx context.Context, owner, family string, now time.Time) ([32]byte, error) {
	scope, err := r.scope(owner, family)
	if err != nil || now.IsZero() {
		return [32]byte{}, ErrPurchaseBinding
	}
	var challenge [32]byte
	if _, err := rand.Read(challenge[:]); err != nil {
		return [32]byte{}, err
	}
	digest := sha256.Sum256(challenge[:])
	err = transaction(ctx, r.Journal.db, func(tx *sql.Tx) error {
		if err := r.Journal.requireRuntime(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM billing_apple_enrollments WHERE expires_ns<=?`, timeNS(now)); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO billing_apple_enrollments
 (incarnation,family,environment,scope,digest,issued_ns,expires_ns,generation)
 SELECT ?,?,?,?,?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=? AND d.state!='cancelled')
 AND NOT EXISTS(SELECT 1 FROM billing_apple_attested_keys k WHERE k.incarnation=? AND k.family=? AND k.environment=?)
 ON CONFLICT(incarnation,family,environment) DO UPDATE SET scope=excluded.scope,digest=excluded.digest,
 issued_ns=excluded.issued_ns,expires_ns=excluded.expires_ns,generation=excluded.generation`,
			owner, family, r.Environment, scope[:], digest[:], timeNS(now),
			timeNS(now.Add(appleEnrollmentLifetime)), r.Journal.generation, owner, owner, family, r.Environment)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n != 1 {
			return ErrUnresolved
		}
		return err
	})
	if err != nil {
		return [32]byte{}, err
	}
	return challenge, nil
}

const appleEnrollmentWhere = ` WHERE incarnation=? AND family=? AND environment=? AND scope=? AND digest=?
 AND issued_ns<=? AND expires_ns>? AND generation=? AND ` + currentGenerationSQL + `=?
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=billing_apple_enrollments.incarnation AND d.state!='cancelled')`

// Enroll verifies Apple's certificate before taking the account or journal
// write lock. It consumes the one-use challenge only when the key is stored.
// The caller must recheck the live verified native session around the write.
func (r AppleCancellationRegistry) Enroll(ctx context.Context, owner, family string, challenge, id [32]byte,
	proof []byte, withLiveSession func(func() error) error, clock func() time.Time) error {
	scope, err := r.scope(owner, family)
	if err != nil || challenge == [32]byte{} || withLiveSession == nil {
		return ErrPurchaseBinding
	}
	if clock == nil {
		clock = time.Now
	}
	digest := sha256.Sum256(challenge[:])
	args := func(at time.Time) []any {
		return []any{owner, family, r.Environment, scope[:], digest[:], timeNS(at), timeNS(at),
			r.Journal.generation, r.Journal.generation}
	}
	at := clock()
	var eligible bool
	if err := r.Journal.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM billing_apple_enrollments`+
		appleEnrollmentWhere+`)`, args(at)...).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return ErrUnresolved
	}
	key, err := r.Verifier.Verify(proof, challenge, id, at)
	if err != nil {
		return err
	}
	point, err := key.ECDH()
	if err != nil {
		return ErrPurchaseBinding
	}
	return withLiveSession(func() error {
		return transaction(ctx, r.Journal.db, func(tx *sql.Tx) error {
			result, err := tx.ExecContext(ctx, `DELETE FROM billing_apple_enrollments`+appleEnrollmentWhere, args(clock())...)
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
			result, err = tx.ExecContext(ctx, `INSERT INTO billing_apple_attested_keys
 (incarnation,family,environment,scope,key_id,point,counter) VALUES(?,?,?,?,?,?,0) ON CONFLICT DO NOTHING`,
				owner, family, r.Environment, scope[:], id[:], point.Bytes())
			if err != nil {
				return err
			}
			n, err = result.RowsAffected()
			if err == nil && n != 1 {
				return ErrPurchaseBinding
			}
			return err
		})
	})
}

// EnrolledInstance returns the server's key identity for the original
// installation. The launch handler must not accept this value from a request.
func (r AppleCancellationRegistry) EnrolledInstance(ctx context.Context, owner, family string) ([32]byte, error) {
	scope, err := r.scope(owner, family)
	if err != nil {
		return [32]byte{}, err
	}
	var saved []byte
	err = r.Journal.db.QueryRowContext(ctx, `SELECT key_id FROM billing_apple_attested_keys
 WHERE incarnation=? AND family=? AND environment=? AND scope=? AND `+currentGenerationSQL+`=?
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=billing_apple_attested_keys.incarnation AND d.state!='cancelled')`,
		owner, family, r.Environment, scope[:], r.Journal.generation).Scan(&saved)
	if errors.Is(err, sql.ErrNoRows) || err == nil && len(saved) != 32 {
		return [32]byte{}, ErrPurchaseBinding
	}
	if err != nil {
		return [32]byte{}, err
	}
	return [32]byte(saved), nil
}

const appleCredentialWhere = ` WHERE incarnation=? AND family=? AND environment=? AND scope=? AND key_id=?
 AND ` + currentGenerationSQL + `=?
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=billing_apple_attested_keys.incarnation AND d.state!='cancelled')`

func (r AppleCancellationRegistry) credentialArgs(c NativeCancellation) ([]any, error) {
	scope, err := r.scope(c.Owner, c.Family)
	if err != nil || c.Attempt.Store != Apple || c.Attempt.Environment != r.Environment || c.AppInstance == [32]byte{} {
		return nil, ErrPurchaseBinding
	}
	return []any{c.Owner, c.Family, r.Environment, scope[:], c.AppInstance[:], r.Journal.generation}, nil
}

func (r AppleCancellationRegistry) CancellationCredential(ctx context.Context, c NativeCancellation) (AppleCancellationCredential, error) {
	args, err := r.credentialArgs(c)
	if err != nil {
		return AppleCancellationCredential{}, err
	}
	var point []byte
	var credential AppleCancellationCredential
	err = r.Journal.db.QueryRowContext(ctx, `SELECT point,counter FROM billing_apple_attested_keys`+
		appleCredentialWhere, args...).Scan(&point, &credential.Count)
	if errors.Is(err, sql.ErrNoRows) {
		return AppleCancellationCredential{}, ErrPurchaseBinding
	}
	if err != nil {
		return AppleCancellationCredential{}, err
	}
	if len(point) != len(credential.Point) {
		return AppleCancellationCredential{}, ErrStoreUnavailable
	}
	copy(credential.Point[:], point)
	return credential, nil
}

func (r AppleCancellationRegistry) AdvanceCancellationCounter(ctx context.Context, c NativeCancellation,
	credential AppleCancellationCredential, next uint32) (bool, error) {
	args, err := r.credentialArgs(c)
	if err != nil || next <= credential.Count || sha256.Sum256(credential.Point[:]) != c.AppInstance {
		return false, ErrPurchaseBinding
	}
	args = append([]any{next}, args...)
	args = append(args, credential.Point[:], credential.Count)
	result, err := r.Journal.db.ExecContext(ctx, `UPDATE billing_apple_attested_keys SET counter=?`+
		appleCredentialWhere+` AND point=? AND counter=?`, args...)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
