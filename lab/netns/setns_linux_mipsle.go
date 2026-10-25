//go:build linux && mipsle

package netns

// sysSetns is the setns syscall number on linux/mipsle (golang.org/x/sys/unix zsysnum_linux_mipsle.go).
const sysSetns = 4344
