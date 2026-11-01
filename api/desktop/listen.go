package desktop

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
)

// defaultListen lets the OS pick the port.
const defaultListen = "127.0.0.1:0"

// checkListen accepts only host:port with a loopback IP literal (127.0.0.0/8
// or ::1) and a numeric port. localhost, host names, wildcard addresses, an
// empty host and a missing port are refused.
func checkListen(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--listen %q: want host:port with a loopback IP address, for example 127.0.0.1:0", addr)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" || ip.Is4In6() || !ip.IsLoopback() {
		return fmt.Errorf("--listen %q: the host must be a loopback IP address (127.0.0.0/8 or ::1); names, localhost and wildcard addresses are not accepted", addr)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("--listen %q: the port must be a number from 0 to 65535", addr)
	}
	return nil
}

// writeAddrFile writes addr to path atomically with mode 0600.
func writeAddrFile(path, addr string) error { return writeFileAtomic(path, addr) }

// writeFileAtomic writes text to path through a private temporary file and a
// rename, so a reader sees the old file or the whole new one, never half.
func writeFileAtomic(path, text string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	// CreateTemp makes the file 0600 (owner-only) already.
	if _, err := tmp.WriteString(text); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
