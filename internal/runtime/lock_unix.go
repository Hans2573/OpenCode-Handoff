//go:build !windows

package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type unixInstanceLock struct {
	file *os.File
}

func AcquireInstanceLock(name string) (InstanceLock, error) {
	path := filepath.Join(os.TempDir(), name+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open instance lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("acquire instance lock: %w", err)
	}
	return &unixInstanceLock{file: file}, nil
}

func (l *unixInstanceLock) Close() error {
	if l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
