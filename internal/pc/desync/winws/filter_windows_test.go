//go:build windows

package winws

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// findWinDivertDLL looks for the pinned WinDivert.dll relative to the repo root.
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

// compileFilter runs WinDivertHelperCompileFilter (layer NETWORK) from the
// user-space DLL: no driver, no administrator rights. It returns the parser's
// error text and position, or nil.
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

func loadCompiler(t *testing.T) *syscall.LazyProc {
	t.Helper()
	path, ok := findWinDivertDLL()
	if !ok {
		t.Skip("WinDivert.dll not found in third_party/bin/windows-amd64 or dist/app (run the fetch/app packaging first)")
	}
	proc := syscall.NewLazyDLL(path).NewProc("WinDivertHelperCompileFilter")
	if err := proc.Find(); err != nil {
		t.Skipf("cannot load WinDivertHelperCompileFilter from %s: %v", path, err)
	}
	return proc
}

// interceptFilter builds the part winws2 adds itself from --wf-tcp-out and
// --wf-udp-out (its form is a stand-in; the raw filter is ANDed with it).
func interceptFilter(tcp, udp string) string {
	var parts []string
	if tcp != "" {
		var ports []string
		for _, p := range strings.Split(tcp, ",") {
			ports = append(ports, "tcp.DstPort == "+p)
		}
		parts = append(parts, "outbound and tcp and ("+strings.Join(ports, " or ")+")")
	}
	if udp != "" {
		var ports []string
		for _, p := range strings.Split(udp, ",") {
			ports = append(ports, "udp.DstPort == "+p)
		}
		parts = append(parts, "outbound and udp and ("+strings.Join(ports, " or ")+")")
	}
	return strings.Join(parts, " or ")
}

// checkRendered compiles every --wf-raw-filter of out, alone and ANDed with the
// interception filter built from its --wf-tcp-out / --wf-udp-out.
func checkRendered(t *testing.T, proc *syscall.LazyProc, args []string) {
	t.Helper()
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
		t.Fatal("no --wf-raw-filter in arguments")
	}
	for _, f := range []string{raw, "(" + interceptFilter(tcp, udp) + ") and (" + raw + ")"} {
		if err := compileFilter(proc, f); err != nil {
			t.Errorf("%v\nfilter: %s", err, f)
		}
	}
}

func TestFilterCompilesInWinDivert(t *testing.T) {
	proc := loadCompiler(t)
	for _, c := range filterCases() {
		t.Run(c.name, func(t *testing.T) {
			f, err := Filter(c.idx, c.ips, c.ports)
			if err != nil {
				t.Fatal(err)
			}
			if err := compileFilter(proc, f); err != nil {
				t.Fatalf("%v\nfilter: %s", err, f)
			}
		})
	}
}

func TestRenderedArgsCompileInWinDivert(t *testing.T) {
	proc := loadCompiler(t)
	for name, in := range map[string]Input{"custom strategy": baseInput(), "profiles": profilesInput()} {
		t.Run(name, func(t *testing.T) {
			out, err := Render(in)
			if err != nil {
				t.Fatal(err)
			}
			checkRendered(t, proc, out.Args)
		})
	}
}
