//go:build !unix

package service

// fileLock stands for the lock of the Unix systems. Elsewhere the window is
// the app's only process that runs tasks (no system scheduler starts
// another), so every lock is free to take.
type fileLock struct{}

func tryLock(path string) (*fileLock, bool, error) { return &fileLock{}, true, nil }

func (l *fileLock) unlock() {}
