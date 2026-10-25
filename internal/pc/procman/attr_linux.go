//go:build linux

package procman

import (
	"errors"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
)

// linuxPlatform starts every child from one goroutine locked to its OS thread:
// Pdeathsig fires when the thread that started the child exits, not the process.
type linuxPlatform struct {
	once sync.Once
	reqs chan launch
}

type launch struct {
	cmd *exec.Cmd
	res chan error
}

func newPlatform() platform { return &linuxPlatform{reqs: make(chan launch)} }

func (p *linuxPlatform) start(cmd *exec.Cmd) error {
	p.once.Do(func() {
		go func() {
			runtime.LockOSThread() // never unlocked: this thread lives as long as the process
			for l := range p.reqs {
				l.res <- l.cmd.Start()
			}
		}()
	})
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	l := launch{cmd: cmd, res: make(chan error, 1)}
	p.reqs <- l
	return <-l.res
}

func (*linuxPlatform) signalStop(cmd *exec.Cmd) (bool, error) {
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	return err == nil, err
}

func (*linuxPlatform) kill(cmd *exec.Cmd) error {
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (*linuxPlatform) needsAdmin(err error) bool { return errors.Is(err, syscall.EPERM) }
