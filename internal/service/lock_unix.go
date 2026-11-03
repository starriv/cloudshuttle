//go:build unix

package service

import (
	"errors"
	"os"
	"syscall"
)

// fileLock is an exclusive lock on a file, which the system lets go of when
// the process ends, however it ends: the processes of the app tell with it
// whether another is alive, and take turns at transfers.
type fileLock struct{ f *os.File }

// tryLock takes the lock on path, or reports false when another process
// holds it.
func tryLock(path string) (*fileLock, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &fileLock{f}, true, nil
}

// unlock lets go of the lock.
func (l *fileLock) unlock() {
	if l != nil {
		l.f.Close()
	}
}
