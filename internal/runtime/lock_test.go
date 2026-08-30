package runtime

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestInstanceLockExcludesSecondOwnerAndCanBeReacquired(t *testing.T) {
	name := fmt.Sprintf("agent-handoff-test-%d", time.Now().UnixNano())
	first, err := AcquireInstanceLock(name)
	if err != nil {
		t.Fatal(err)
	}
	second, err := AcquireInstanceLock(name)
	if second != nil {
		_ = second.Close()
		t.Fatal("second lock unexpectedly succeeded")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second lock error = %v, want ErrAlreadyRunning", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reacquired, err := AcquireInstanceLock(name)
	if err != nil {
		t.Fatalf("reacquire after close: %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatal(err)
	}
}
