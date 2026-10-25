//go:build linux && amd64

package netns

// sysSetns is the setns syscall number on linux/amd64 (golang.org/x/sys/unix zsysnum_linux_amd64.go).
const sysSetns = 308
