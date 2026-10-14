package pc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockInstance takes an exclusive flock on <Run>/splitter.lock. The kernel
// drops it when the process exits, so no stale lock survives a crash.
func lockInstance(dir string) (release func(), err error) {
	f, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("instance lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errAnotherInstance
		}
		return nil, fmt.Errorf("instance lock: %w", err)
	}
	return func() { _ = f.Close() }, nil
}
