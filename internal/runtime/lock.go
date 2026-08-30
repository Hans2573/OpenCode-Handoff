package runtime

import "errors"

var ErrAlreadyRunning = errors.New("another Handoff engine is already running")

type InstanceLock interface {
	Close() error
}
