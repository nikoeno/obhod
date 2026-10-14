package pc

import (
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
)

// errorSharingViolation is ERROR_SHARING_VIOLATION (winerror.h).
const errorSharingViolation = syscall.Errno(32)

// lockInstance opens <Run>/splitter.lock with no sharing. A second app opens
// it with ERROR_SHARING_VIOLATION until the first one exits (Windows closes the
// handle of a dead process, so no stale lock survives a crash).
func lockInstance(dir string) (release func(), err error) {
	name, err := syscall.UTF16PtrFromString(filepath.Join(dir, lockFileName))
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, errorSharingViolation) {
			return nil, errAnotherInstance
		}
		return nil, fmt.Errorf("instance lock: %w", err)
	}
	return func() { _ = syscall.CloseHandle(h) }, nil
}
