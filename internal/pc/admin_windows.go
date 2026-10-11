//go:build windows

package pc

import (
	"syscall"
	"unsafe"
)

// tokenElevation is TOKEN_INFORMATION_CLASS TokenElevation.
const tokenElevation = 20

// isAdmin reports whether the process token is elevated.
func isAdmin() (bool, error) {
	var tok syscall.Token
	proc, err := syscall.GetCurrentProcess()
	if err != nil {
		return false, err
	}
	if err := syscall.OpenProcessToken(proc, syscall.TOKEN_QUERY, &tok); err != nil {
		return false, err
	}
	defer tok.Close()
	var elevated uint32
	var n uint32
	if err := syscall.GetTokenInformation(tok, tokenElevation, (*byte)(unsafe.Pointer(&elevated)), uint32(unsafe.Sizeof(elevated)), &n); err != nil {
		return false, err
	}
	return elevated != 0, nil
}

// elevationHint is the exact way to start the app with rights, printed for the human.
const elevationHint = "start splitter from an elevated terminal (Run as administrator)"
