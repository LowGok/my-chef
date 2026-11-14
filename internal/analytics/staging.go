package analytics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// WithAppStaging registers raw application snapshot bytes before they are
// written. The callback sanitizes the snapshot before publishing any operational
// backup outside the root. Startup maintenance destroys an interrupted stage.
func (s *Service) WithAppStaging(ctx context.Context, work func(string) error) error {
	if e := s.mu.LockContext(ctx); e != nil {
		return e
	}
	defer s.mu.Unlock()
	if s.closed {
		return ErrPrivacy
	}
	if e := s.maintainLocked(ctx); e != nil {
		return e
	}
	id, e := s.uniqueID(ctx, "stg")
	if e != nil {
		return e
	}
	path := filepath.Join(s.root, id+".sqlite")
	a, e := s.readAnchor(ctx)
	if e != nil {
		return e
	}
	if _, e = s.privacy.ExecContext(ctx, "INSERT INTO copies(path,kind,reporting,generation,created,state) VALUES(?,'staging',0,?,?,'building')", path, a.Generation, s.now().Unix()); e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	e = work(path)
	return errors.Join(e, s.destroyCopy(context.WithoutCancel(ctx), path))
}
