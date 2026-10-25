//go:build linux && arm64

package netns

// sysSetns is the setns syscall number on linux/arm64 (golang.org/x/sys/unix zsysnum_linux_arm64.go).
const sysSetns = 268
