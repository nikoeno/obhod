package netif

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestOSVirtualSysfs(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, typ string, tun bool) {
		d := filepath.Join(dir, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "type"), []byte(typ+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if tun {
			if err := os.WriteFile(filepath.Join(d, "tun_flags"), []byte("0x1001\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("eth0", "1", false)
	mk("splitter", "65534", true) // the app's own TUN: no name fragment matches
	mk("tap9", "1", true)
	mk("wg-home", "65534", false)
	mk("br0", "1", false)
	for name, want := range map[string][2]bool{
		"eth0": {false, true}, "splitter": {true, true}, "tap9": {true, true},
		"wg-home": {true, true}, "br0": {false, true}, "missing0": {false, false},
	} {
		v, k := osVirtualAt(dir, net.Interface{Name: name})
		if v != want[0] || k != want[1] {
			t.Errorf("%s: virtual=%v known=%v, want %v", name, v, k, want)
		}
	}
	if v, k := osVirtualAt(dir, net.Interface{Name: "splitter"}); !combineVirtual(net.Interface{Name: "splitter"}, v, k) {
		t.Error("the app's TUN counts as physical")
	}
}
