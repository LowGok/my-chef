package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

const privacySchema = `
CREATE TABLE IF NOT EXISTS billing_privacy_key(
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), key BLOB NOT NULL CHECK(length(key)=32)
) STRICT;
CREATE TABLE IF NOT EXISTS billing_replay_markers(
 digest BLOB PRIMARY KEY NOT NULL CHECK(length(digest)=32)
) STRICT;
CREATE TABLE IF NOT EXISTS billing_app_origins(
 device TEXT NOT NULL, inode TEXT NOT NULL, lifecycle_binding TEXT NOT NULL, path_scope TEXT NOT NULL CHECK(length(path_scope)=64),
 generation TEXT NOT NULL UNIQUE CHECK(length(generation)=32), PRIMARY KEY(device,inode,lifecycle_binding,path_scope)
) STRICT;
CREATE TABLE IF NOT EXISTS billing_deletion_origins(
 operation TEXT PRIMARY KEY NOT NULL REFERENCES lifecycle_deletions(operation),
 generation TEXT NOT NULL REFERENCES billing_app_origins(generation), baseline BLOB NOT NULL CHECK(length(baseline)=32)
) STRICT;
`

func ownerFenced(ctx context.Context, db reader, inc string) (bool, error) {
	var blocked bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lifecycle_deletions WHERE incarnation=? AND state!='cancelled')`, inc).Scan(&blocked)
	return blocked, err
}

func billingPrivacyKey(ctx context.Context, db reader) ([]byte, error) {
	var key []byte
	if err := db.QueryRowContext(ctx, `SELECT key FROM billing_privacy_key WHERE singleton=1`).Scan(&key); err != nil || len(key) != 32 {
		return nil, ErrStoreUnavailable
	}
	return key, nil
}

// CheckDeletionStorage verifies the explicit external migration before the app
// can delete data. Normal service startup does not create billing tables.
func CheckDeletionStorage(ctx context.Context, db *sql.DB) error {
	if _, err := billingPrivacyKey(ctx, db); err != nil {
		return err
	}
	var count int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name IN(
 'billing_attempts','billing_subscriptions','billing_periods','billing_apple_polls','billing_apple_notices','billing_replay_markers',
 'billing_play_notices','billing_play_polls','billing_play_replacements','billing_app_origins','billing_deletion_origins','billing_runtime','billing_runtime_proofs')`).Scan(&count)
	if err != nil || count != 13 {
		return ErrStoreUnavailable
	}
	return nil
}

func replayDigest(key []byte, env Environment, store Store, kind, value string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(h, "mychef/billing/deleted/v1\x00%s\x00%s\x00%s\x00%s", env, store, kind, value)
	return h.Sum(nil)
}

func rejectedReplay(ctx context.Context, tx *sql.Tx, p VerifiedPeriod) (bool, error) {
	key, err := billingPrivacyKey(ctx, tx)
	if err != nil {
		return false, err
	}
	var match bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM billing_replay_markers WHERE digest IN (?,?,?))`,
		replayDigest(key, p.Environment, p.Store, "binding", p.Binding), replayDigest(key, p.Environment, p.Store, "subscription", p.Subscription),
		replayDigest(key, p.Environment, p.Store, "transaction", p.Transaction)).Scan(&match)
	return match, err
}

// PurgeForDeletion runs in the external transaction that commits the app's
// matching deletion marker. Only unlinkable purchase digests survive.
func PurgeForDeletion(ctx context.Context, tx *sql.Tx, operation, incarnation string) error {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT state FROM lifecycle_deletions WHERE operation=? AND incarnation=?`, operation, incarnation).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) || err == nil && state != "prepared" {
		return ErrPurchaseBinding
	}
	if err != nil {
		return err
	}
	key, err := billingPrivacyKey(ctx, tx)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT environment,store,'binding',binding FROM billing_attempts WHERE incarnation=?
 UNION ALL SELECT environment,store,'subscription',subscription FROM billing_subscriptions WHERE incarnation=?
 UNION ALL SELECT p.environment,p.store,'transaction',p.proof FROM billing_periods p
 JOIN billing_subscriptions s USING(environment,store,subscription) WHERE s.incarnation=?
 UNION ALL SELECT n.environment,'apple','transaction',n.transaction_id FROM billing_apple_notices n
 JOIN billing_attempts a USING(binding) WHERE a.incarnation=?`, incarnation, incarnation, incarnation, incarnation)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var markers [][]byte
	for rows.Next() {
		var env Environment
		var store Store
		var kind, value string
		if err := rows.Scan(&env, &store, &kind, &value); err != nil {
			return err
		}
		markers = append(markers, replayDigest(key, env, store, kind, value))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, marker := range markers {
		if _, err := tx.ExecContext(ctx, `INSERT INTO billing_replay_markers(digest) VALUES(?) ON CONFLICT DO NOTHING`, marker); err != nil {
			return err
		}
	}
	for _, query := range []string{
		`DELETE FROM billing_play_replacements WHERE binding IN (SELECT binding FROM billing_attempts WHERE incarnation=?)`,
		`DELETE FROM billing_play_polls WHERE binding IN (SELECT binding FROM billing_attempts WHERE incarnation=?)`,
		`DELETE FROM billing_play_notices WHERE binding IN (SELECT binding FROM billing_attempts WHERE incarnation=?)`,
		`DELETE FROM billing_apple_notices WHERE binding IN (SELECT binding FROM billing_attempts WHERE incarnation=?)`,
		`DELETE FROM billing_apple_polls WHERE binding IN (SELECT binding FROM billing_subscriptions WHERE incarnation=?)`,
		`DELETE FROM billing_periods WHERE (environment,store,subscription) IN (SELECT environment,store,subscription FROM billing_subscriptions WHERE incarnation=?)`,
		`DELETE FROM billing_subscriptions WHERE incarnation=?`,
		`DELETE FROM billing_attempts WHERE incarnation=?`,
	} {
		if _, err := tx.ExecContext(ctx, query, incarnation); err != nil {
			return err
		}
	}
	return nil
}
