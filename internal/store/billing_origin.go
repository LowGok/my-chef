package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"syscall"

	"gorm.io/gorm"
)

type deletionOrigin struct {
	device, inode, pathScope string
	baseline                 []byte
}

func (s *Store) originalFile(current os.FileInfo) (string, string, error) {
	if current == nil || s.identity == nil || !current.Mode().IsRegular() || !os.SameFile(current, s.identity) {
		return "", "", ErrLifecycleUncertain
	}
	info, ok := current.Sys().(*syscall.Stat_t)
	if !ok {
		return "", "", ErrLifecycleUncertain
	}
	return strconv.FormatUint(uint64(info.Dev), 16), strconv.FormatUint(uint64(info.Ino), 16), nil
}

func (j *LifecycleJournal) unresolvedDeletion(ctx context.Context) error {
	var pending bool
	if err := j.store.SQL.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lifecycle_deletions WHERE state='prepared')`).Scan(&pending); err != nil {
		return ErrLifecycleJournal
	}
	if pending {
		return ErrLifecycleUncertain
	}
	return nil
}

// CheckServing refuses new app work while a deletion outcome is unresolved.
// It does not accept the absence of an app receipt as a rollback proof.
func (j *LifecycleJournal) CheckServing(ctx context.Context) error {
	if err := j.CheckAvailable(ctx); err != nil {
		return err
	}
	return j.unresolvedDeletion(ctx)
}

// checkpointDeletionOrigin owns the application's only SQLite connection and
// the caller holds AccountWork. The digest is a diagnostic baseline, never an
// authorization to clear a prepared intent after a process restart.
func (j *LifecycleJournal) checkpointDeletionOrigin(ctx context.Context, db *gorm.DB, op, inc string) (deletionOrigin, error) {
	if err := j.CheckServing(ctx); err != nil {
		return deletionOrigin{}, err
	}
	current, err := os.Stat(j.app.Path)
	if err != nil {
		return deletionOrigin{}, ErrLifecycleUncertain
	}
	dev, ino, err := j.app.originalFile(current)
	if err != nil {
		return deletionOrigin{}, err
	}
	var busy, log, done int
	if err := db.Raw("PRAGMA wal_checkpoint(TRUNCATE)").Row().Scan(&busy, &log, &done); err != nil || busy != 0 || log != 0 || done != 0 {
		return deletionOrigin{}, ErrLifecycleUncertain
	}
	// Hash the checkpointed file from an open handle, then recheck both the
	// handle and path. Only the protected sidecar holds the digest and key.
	file, err := os.Open(j.app.Path)
	if err != nil {
		return deletionOrigin{}, ErrLifecycleUncertain
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(current, opened) {
		return deletionOrigin{}, ErrLifecycleUncertain
	}
	key, err := j.deletionOriginKey(ctx)
	if err != nil {
		return deletionOrigin{}, err
	}
	scope := deletionPathScope(key, j.app.Path)
	digest := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(digest, "mychef/deletion/baseline/v1\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00", j.binding, dev, ino, scope, op, inc)
	if _, err := io.Copy(digest, file); err != nil || ctx.Err() != nil {
		return deletionOrigin{}, ErrLifecycleUncertain
	}
	after, err := os.Stat(j.app.Path)
	if err != nil || !os.SameFile(opened, after) || after.Size() != opened.Size() || after.ModTime() != opened.ModTime() {
		return deletionOrigin{}, ErrLifecycleUncertain
	}
	return deletionOrigin{device: dev, inode: ino, pathScope: scope, baseline: digest.Sum(nil)}, nil
}

func (j *LifecycleJournal) deletionOriginKey(ctx context.Context) ([]byte, error) {
	var key []byte
	if err := j.store.SQL.QueryRowContext(ctx, `SELECT key FROM billing_privacy_key WHERE singleton=1`).Scan(&key); err != nil || len(key) != 32 {
		return nil, ErrLifecycleJournal
	}
	return key, nil
}

func deletionPathScope(key []byte, path string) string {
	digest := hmac.New(sha256.New, key)
	_, _ = io.WriteString(digest, "mychef/deletion/volume-path/v1\x00"+path)
	return hex.EncodeToString(digest.Sum(nil))
}

func (j *LifecycleJournal) deletionOriginMatches(ctx context.Context, op string) error {
	current, err := os.Stat(j.app.Path)
	if err != nil {
		return ErrLifecycleUncertain
	}
	dev, ino, err := j.app.originalFile(current)
	if err != nil {
		return err
	}
	key, err := j.deletionOriginKey(ctx)
	if err != nil {
		return err
	}
	var exists bool
	if err := j.store.SQL.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM billing_deletion_origins d
 JOIN billing_app_origins a USING(generation) WHERE d.operation=? AND a.device=? AND a.inode=? AND a.lifecycle_binding=? AND a.path_scope=?)`,
		op, dev, ino, j.binding, deletionPathScope(key, j.app.Path)).Scan(&exists); err != nil {
		return ErrLifecycleJournal
	}
	if !exists {
		return ErrLifecycleUncertain
	}
	return nil
}
