package procman

import "os/exec"

// platform holds everything that differs per OS: how a child is started so that
// it dies with the manager's process, and how it is stopped.
type platform interface {
	// start starts cmd with the OS attributes that tie it to our lifetime.
	start(cmd *exec.Cmd) error
	// signalStop asks the process to exit. It reports false when the OS has no
	// graceful way (Windows), and the caller must kill at once.
	signalStop(cmd *exec.Cmd) (sent bool, err error)
	// kill terminates the process (and its group where the OS has one).
	kill(cmd *exec.Cmd) error
	// needsAdmin reports whether a start error means missing admin rights.
	needsAdmin(err error) bool
}
