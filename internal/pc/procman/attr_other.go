//go:build !windows && !linux

package procman

import "os/exec"

type otherPlatform struct{}

func newPlatform() platform { return otherPlatform{} }

// start starts the child plainly; on this OS nothing ties it to our lifetime.
func (otherPlatform) start(cmd *exec.Cmd) error { return cmd.Start() }

func (otherPlatform) signalStop(*exec.Cmd) (bool, error) { return false, nil }

func (otherPlatform) kill(cmd *exec.Cmd) error { return cmd.Process.Kill() }

func (otherPlatform) needsAdmin(error) bool { return false }
