package analytics

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

func reclaimSQL(ctx context.Context, db *sql.DB) error {
	if _, e := db.ExecContext(ctx, "PRAGMA secure_delete=ON; PRAGMA temp_store=MEMORY; VACUUM;"); e != nil {
		return e
	}
	var busy, log, done int
	if e := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &done); e != nil || busy != 0 {
		return ErrPrivacy
	}
	return nil
}
func (s *Service) checkInventory(ctx context.Context) error {
	rows, e := s.privacy.QueryContext(ctx, "SELECT path FROM copies WHERE state!='deleted'")
	if e != nil {
		return e
	}
	allowed := map[string]bool{filepath.Join(s.root, "current"): true, filepath.Join(s.root, "current.pending"): true}
	for rows.Next() {
		var p string
		if e = rows.Scan(&p); e != nil {
			_ = rows.Close()
			return e
		}
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			allowed[p+suffix] = true
		}
	}
	if e = rows.Close(); e != nil {
		return e
	}
	return filepath.WalkDir(s.root, func(path string, entry fs.DirEntry, e error) error {
		if e != nil {
			return ErrPrivacy
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrPrivacy
		}
		if entry.IsDir() {
			return privatePath(path, true)
		}
		if !allowed[path] {
			return ErrPrivacy
		}
		return privatePath(path, false)
	})
}
func (s *Service) publishCurrent(ctx context.Context, path string) error {
	pointer := filepath.Join(s.root, "current")
	pending := pointer + ".pending"
	if e := os.WriteFile(pending, []byte(path), 0600); e != nil {
		return e
	}
	f, e := os.Open(pending)
	if e != nil {
		return e
	}
	e = errors.Join(f.Sync(), f.Close())
	if e != nil {
		return e
	}
	if e = os.Rename(pending, pointer); e != nil {
		return e
	}
	dir, e := os.Open(s.root)
	if e != nil {
		return e
	}
	e = errors.Join(dir.Sync(), dir.Close())
	if e != nil {
		return e
	}
	rows, e := s.privacy.QueryContext(ctx, "SELECT path FROM copies WHERE kind='extract' AND state!='deleted' AND path!=?", path)
	if e != nil {
		return e
	}
	var old []string
	for rows.Next() {
		var p string
		if e = rows.Scan(&p); e != nil {
			_ = rows.Close()
			return e
		}
		old = append(old, p)
	}
	if e = rows.Close(); e != nil {
		return e
	}
	for _, p := range old {
		if e = s.destroyCopy(ctx, p); e != nil {
			return e
		}
	}
	return nil
}
