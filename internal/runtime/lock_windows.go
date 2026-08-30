//go:build windows

package runtime

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

type windowsInstanceLock struct {
	handle windows.Handle
}

func AcquireInstanceLock(name string) (InstanceLock, error) {
	mutexName, err := windows.UTF16PtrFromString("Local\\" + name)
	if err != nil {
		return nil, fmt.Errorf("encode instance lock name: %w", err)
	}
	handle, err := windows.CreateMutex(nil, false, mutexName)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		_ = windows.CloseHandle(handle)
		return nil, ErrAlreadyRunning
	}
	if err != nil {
		return nil, fmt.Errorf("create instance lock: %w", err)
	}
	return &windowsInstanceLock{handle: handle}, nil
}

func (l *windowsInstanceLock) Close() error {
	if l.handle == 0 {
		return nil
	}
	err := windows.CloseHandle(l.handle)
	l.handle = 0
	return err
}
