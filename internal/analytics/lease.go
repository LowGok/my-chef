package analytics

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const ReadLeaseLimit = 30 * time.Second

type privacyMutex struct {
	base   sync.Mutex
	file   *os.File
	failed atomic.Bool
}

func waitLease(ctx context.Context, try func() (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, ReadLeaseLimit)
	defer cancel()
	timer := time.NewTicker(5 * time.Millisecond)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, err := try()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func flockContext(ctx context.Context, file *os.File) error {
	return waitLease(ctx, func() (bool, error) {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return false, nil
		}
		return err == nil, err
	})
}
func (m *privacyMutex) LockContext(ctx context.Context) error {
	if err := waitLease(ctx, func() (bool, error) { return m.base.TryLock(), nil }); err != nil {
		return err
	}
	m.failed.Store(false)
	if m.file != nil {
		if err := flockContext(ctx, m.file); err != nil {
			m.base.Unlock()
			return err
		}
	}
	return nil
}
func (m *privacyMutex) Unlock() {
	if m.file != nil && !m.failed.Load() {
		if err := syscall.Flock(int(m.file.Fd()), syscall.LOCK_UN); err != nil {
			m.failed.Store(true)
		}
	}
	m.base.Unlock()
}
