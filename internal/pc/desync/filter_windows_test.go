//go:build windows

package desync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// The pinned WinDivert.dll (user space only: no driver, no administrator
// rights) compiles every --wf-raw-filter we render, as winws2 would.

func findWinDivertDLL() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			for _, rel := range []string{
				filepath.Join("third_party", "bin", "windows-amd64", "WinDivert.dll"),
				filepath.Join("dist", "app", "WinDivert.dll"),
			} {
				p := filepath.Join(dir, rel)
				if _, err := os.Stat(p); err == nil {
					return p, true
				}
			}
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func compileFilter(proc *syscall.LazyProc, filter string) error {
	f, err := syscall.BytePtrFromString(filter)
	if err != nil {
		return err
	}
	obj := make([]byte, 64*1024)
	var errStr *byte
	var errPos uint32
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(f)), 0 /* WINDIVERT_LAYER_NETWORK */, uintptr(unsafe.Pointer(&obj[0])),
		uintptr(len(obj)), uintptr(unsafe.Pointer(&errStr)), uintptr(unsafe.Pointer(&errPos)))
	if r != 0 {
		return nil
	}
	msg := "unknown error"
	if errStr != nil {
		var b []byte
		for p := unsafe.Pointer(errStr); *(*byte)(p) != 0 && len(b) < 256; p = unsafe.Add(p, 1) {
			b = append(b, *(*byte)(p))
		}
		msg = string(b)
	}
	return fmt.Errorf("WinDivert: %s at position %d", msg, errPos)
}

func interceptFilter(tcp, udp string) string {
	var parts []string
	for _, g := range []struct{ proto, list string }{{"tcp", tcp}, {"udp", udp}} {
		if g.list == "" {
			continue
		}
		var ports []string
		for _, p := range strings.Split(g.list, ",") {
			ports = append(ports, g.proto+".DstPort == "+p)
		}
		parts = append(parts, "outbound and "+g.proto+" and ("+strings.Join(ports, " or ")+")")
	}
	return strings.Join(parts, " or ")
}

func TestPresetFiltersCompileInWinDivert(t *testing.T) {
	path, ok := findWinDivertDLL()
	if !ok {
		t.Skip("WinDivert.dll not found in third_party/bin/windows-amd64 or dist/app (run the fetch/app packaging first)")
	}
	proc := syscall.NewLazyDLL(path).NewProc("WinDivertHelperCompileFilter")
	if err := proc.Find(); err != nil {
		t.Skipf("cannot load WinDivertHelperCompileFilter from %s: %v", path, err)
	}
	for name, args := range winwsRenders(t) {
		t.Run(name, func(t *testing.T) {
			var raw, tcp, udp string
			for _, a := range args {
				switch {
				case strings.HasPrefix(a, "--wf-raw-filter="):
					raw = strings.TrimPrefix(a, "--wf-raw-filter=")
				case strings.HasPrefix(a, "--wf-tcp-out="):
					tcp = strings.TrimPrefix(a, "--wf-tcp-out=")
				case strings.HasPrefix(a, "--wf-udp-out="):
					udp = strings.TrimPrefix(a, "--wf-udp-out=")
				}
			}
			if raw == "" {
				t.Fatal("no --wf-raw-filter")
			}
			for _, f := range []string{raw, "(" + interceptFilter(tcp, udp) + ") and (" + raw + ")"} {
				if err := compileFilter(proc, f); err != nil {
					t.Errorf("%v\nfilter: %s", err, f)
				}
			}
		})
	}
}
