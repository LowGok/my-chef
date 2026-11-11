package store

import (
	"fmt"
	"os"
	"syscall"
)

// LockApplication prevents a second server or schema migration from resetting active jobs.
// Backup uses a separate SQLite connection and intentionally does not take this lock.
func LockApplication(path string) (func(), error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("database is already in use")
	}
	return func() { _ = f.Close() }, nil
}
