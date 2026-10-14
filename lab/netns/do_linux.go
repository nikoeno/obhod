//go:build linux

package netns

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
)

// cloneNewNet is CLONE_NEWNET from <linux/sched.h>, the nstype argument of setns.
const cloneNewNet = 0x40000000

// Do runs fn on a locked OS thread switched into ns, then switches back. Sockets created inside fn
// stay in ns after it returns, so listeners and the NFQUEUE socket can be served from any goroutine.
// Goroutines started by fn run on other threads and are not in ns. If switching back fails the
// thread stays locked, so the Go runtime discards it instead of reusing a thread in the wrong
// namespace. On Linux architectures without a known setns number Do returns errors.ErrUnsupported.
func (ns *NS) Do(fn func() error) error {
	if ns == nil {
		return errors.New("netns: nil namespace")
	}
	if _, _, err := splitFullName(ns.Name); err != nil {
		return err
	}
	if sysSetns == 0 {
		return fmt.Errorf("netns: setns: %w", errors.ErrUnsupported)
	}
	target, err := os.Open(filepath.Join(defaultRunDir, ns.Name))
	if err != nil {
		return fmt.Errorf("netns: %w", err)
	}
	defer target.Close()

	runtime.LockOSThread()
	orig, err := os.Open("/proc/self/task/" + strconv.Itoa(syscall.Gettid()) + "/ns/net")
	if err != nil {
		runtime.UnlockOSThread()
		return fmt.Errorf("netns: %w", err)
	}
	defer orig.Close()

	if err := setns(target.Fd()); err != nil {
		runtime.UnlockOSThread()
		return fmt.Errorf("netns: setns %s: %w", ns.Name, err)
	}
	fnErr := fn()
	if err := setns(orig.Fd()); err != nil {
		// Leave the thread locked: the runtime terminates it when this goroutine exits.
		return errors.Join(fnErr, fmt.Errorf("netns: setns back from %s: %w", ns.Name, err))
	}
	runtime.UnlockOSThread()
	return fnErr
}

// setns moves the calling thread into the network namespace referred to by fd.
func setns(fd uintptr) error {
	if _, _, errno := syscall.Syscall(sysSetns, fd, cloneNewNet, 0); errno != 0 {
		return errno
	}
	return nil
}
