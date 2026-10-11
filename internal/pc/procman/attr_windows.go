//go:build windows

package procman

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

const (
	errorElevationRequired = syscall.Errno(740)
	jobObjectExtendedLimit = 9      // JobObjectExtendedLimitInformation
	jobLimitKillOnClose    = 0x2000 // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	processSetQuota        = 0x0100
	processTerminate       = 0x0001
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObject    = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJob  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJob = kernel32.NewProc("AssignProcessToJobObject")
)

// Mirrors of the Win32 structures; the field order and sizes follow winnt.h.
type ioCounters struct{ a, b, c, d, e, f uint64 }

type basicLimit struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type extendedLimit struct {
	BasicLimitInformation basicLimit
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// windowsPlatform assigns every child to one Job Object with KILL_ON_JOB_CLOSE.
// The handle is never closed: when our process dies, Windows closes it and
// terminates every child. A child could escape only in the gap between its
// start and the assignment, which our children do not use to fork.
type windowsPlatform struct {
	once sync.Once
	job  syscall.Handle
	err  error
}

func newPlatform() platform { return &windowsPlatform{} }

func (p *windowsPlatform) ensureJob() (syscall.Handle, error) {
	p.once.Do(func() {
		h, _, e := procCreateJobObject.Call(0, 0)
		if h == 0 {
			p.err = fmt.Errorf("CreateJobObject: %w", e)
			return
		}
		info := extendedLimit{}
		info.BasicLimitInformation.LimitFlags = jobLimitKillOnClose
		r, _, e := procSetInformationJob.Call(h, jobObjectExtendedLimit,
			uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
		if r == 0 {
			_ = syscall.CloseHandle(syscall.Handle(h))
			p.err = fmt.Errorf("SetInformationJobObject: %w", e)
			return
		}
		p.job = syscall.Handle(h)
	})
	return p.job, p.err
}

func (p *windowsPlatform) start(cmd *exec.Cmd) error {
	job, err := p.ensureJob()
	if err != nil {
		return fmt.Errorf("procman: job object: %w", err)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	h, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(cmd.Process.Pid))
	if err == nil {
		defer syscall.CloseHandle(h)
		var r uintptr
		var e error
		r, _, e = procAssignProcessToJob.Call(uintptr(job), uintptr(h))
		if r == 0 {
			err = e
		}
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("procman: assign to job object: %w", err)
	}
	return nil
}

// signalStop reports false: Windows has no console signal for a child without
// a console, so the caller terminates it at once.
func (*windowsPlatform) signalStop(*exec.Cmd) (bool, error) { return false, nil }

func (*windowsPlatform) kill(cmd *exec.Cmd) error { return cmd.Process.Kill() }

func (*windowsPlatform) needsAdmin(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == errorElevationRequired || errno == syscall.ERROR_ACCESS_DENIED
	}
	return false
}
