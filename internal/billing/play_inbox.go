package billing

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"errors"
	"time"
)

const playInboxSchema = `
CREATE TABLE IF NOT EXISTS billing_play_notices(
 environment TEXT NOT NULL CHECK(environment IN ('sandbox','production')),
 event_id TEXT NOT NULL,
 binding TEXT NOT NULL REFERENCES billing_attempts(binding) ON DELETE CASCADE,
 token_digest TEXT NOT NULL CHECK(length(token_digest)=64),
 ciphertext BLOB NOT NULL CHECK(length(ciphertext) BETWEEN 29 AND 2076),
 due_ns INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
 lease TEXT NOT NULL DEFAULT '', lease_ns INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL DEFAULT 'ready' CHECK(state IN ('ready','done','failed')),
 PRIMARY KEY(environment,event_id)
) STRICT;
CREATE INDEX IF NOT EXISTS billing_play_notices_binding ON billing_play_notices(binding);
CREATE INDEX IF NOT EXISTS billing_play_notices_due ON billing_play_notices(environment,state,due_ns,lease_ns);
`

// PlayInbox records minimum authenticated routing data for deferred
// reconciliation. Validation never grants access or acknowledges a purchase.
// Its encryption key is supplied separately from the protected journal.
type PlayInbox struct {
	journal  *Journal
	env      Environment
	offers   []Offer
	provider RestoreProvider
	seal     cipher.AEAD
}

func NewPlayInbox(j *Journal, env Environment, offers []Offer, provider RestoreProvider, key []byte) (*PlayInbox, error) {
	if j == nil || provider == nil || !validEnvironment(env) || len(key) != 32 || len(offers) == 0 {
		return nil, ErrStoreUnavailable
	}
	hasOffer := false
	for _, offer := range offers {
		if offer.Store == Play && offer.Environment == env && offer.App != "" &&
			offer.Product != "" && offer.BasePlan != "" && validOffer(Play, env, offer.Interval) {
			hasOffer = true
		}
	}
	if !hasOffer {
		return nil, ErrStoreUnavailable
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrStoreUnavailable
	}
	seal, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, ErrStoreUnavailable
	}
	return &PlayInbox{journal: j, env: env, offers: append([]Offer(nil), offers...), provider: provider, seal: seal}, nil
}

func (i *PlayInbox) context(id, accountBinding, digest string) []byte {
	return []byte("mychef/play/inbox/v1\x00" + string(i.env) + "\x00" + id + "\x00" + accountBinding + "\x00" + digest)
}

func (i *PlayInbox) open(id, accountBinding, digest string, ciphertext []byte) (string, error) {
	cleartext, err := i.seal.Open(nil, nil, ciphertext, i.context(id, accountBinding, digest))
	if err != nil {
		return "", ErrPurchaseBinding
	}
	defer clear(cleartext)
	token := string(cleartext)
	if !validPlayToken(token) || playTokenDigest(token) != digest {
		return "", ErrPurchaseBinding
	}
	return token, nil
}

// Retain returns success for an identical authenticated redelivery. A new
// message must match an existing live checkout before it enters the journal.
func (i *PlayInbox) Retain(ctx context.Context, n PlayNotice, now time.Time) error {
	if !pubsubMessage.MatchString(n.ID) || !validPlayToken(n.Token) || now.IsZero() {
		return ErrPurchaseBinding
	}
	if err := i.journal.CurrentRuntime(ctx); err != nil {
		return err
	}
	proof, err := i.provider.VerifyExisting(ctx, n.Token)
	var signup *playSignup
	if errors.As(err, &signup) {
		return i.retainSignup(ctx, signup, n.Token, "")
	}
	if errors.Is(err, ErrUnresolved) {
		return i.retainReplacement(ctx, "", n.Token, nil, func(ctx context.Context, tx *sql.Tx, r PlayReplacement) error {
			// Reconciliation now follows the durable, leased poll row.
			return i.recordNotice(ctx, tx, n, r.Binding, r.Current, now, "done")
		})
	}
	if err != nil {
		return err
	}
	period := proof.Period
	digest := playTokenDigest(n.Token)
	if period.Environment != i.env || period.Store != Play || !accountUUID.MatchString(period.Binding) ||
		period.Subscription != digest {
		return ErrPurchaseBinding
	}
	approved := false
	for _, offer := range i.offers {
		if offer.matches(proof) {
			approved = true
			break
		}
	}
	if !approved {
		return ErrPurchaseBinding
	}
	return transaction(ctx, i.journal.db, func(tx *sql.Tx) error {
		return i.recordNotice(ctx, tx, n, period.Binding, digest, now, "ready")
	})
}

func (i *PlayInbox) recordNotice(ctx context.Context, tx *sql.Tx, n PlayNotice, accountBinding, digest string, now time.Time, state string) error {
	raw := []byte(n.Token)
	ciphertext := i.seal.Seal(nil, nil, raw, i.context(n.ID, accountBinding, digest))
	clear(raw)
	if _, err := tx.ExecContext(ctx, `INSERT INTO billing_play_notices(environment,event_id,binding,token_digest,ciphertext,due_ns,state)
 SELECT environment,?,binding,?,?,?,? FROM billing_attempts
 WHERE binding=? AND environment=? AND store='play' AND state IN ('launched','fulfilled')
 AND NOT EXISTS(SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=billing_attempts.incarnation AND d.state!='cancelled')
 AND `+currentGenerationSQL+`=?
 ON CONFLICT(environment,event_id) DO NOTHING`,
		n.ID, digest, ciphertext, timeNS(now), state, accountBinding, i.env, i.journal.generation); err != nil {
		return err
	}
	if err := i.journal.requireRuntime(ctx, tx); err != nil {
		return err
	}
	var foundBinding, foundDigest string
	var stored []byte
	err := tx.QueryRowContext(ctx, `SELECT n.binding,n.token_digest,n.ciphertext FROM billing_play_notices n
 JOIN billing_attempts a USING(binding)
 WHERE n.environment=? AND n.event_id=? AND NOT EXISTS(
  SELECT 1 FROM lifecycle_deletions d WHERE d.incarnation=a.incarnation AND d.state!='cancelled')`,
		i.env, n.ID).Scan(&foundBinding, &foundDigest, &stored)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (foundBinding != accountBinding || foundDigest != digest) {
		return ErrPurchaseBinding
	}
	if err != nil {
		return err
	}
	_, err = i.open(n.ID, foundBinding, foundDigest, stored)
	return err
}
