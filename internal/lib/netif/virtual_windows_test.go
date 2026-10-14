package netif

import (
	"net"
	"strings"
	"testing"
	"unicode/utf16"
	"unsafe"
)

// The loopback pseudo-interface is never hardware; this also checks that the
// MIB_IF_ROW2 offsets read a meaningful flag on this machine.
func TestOSVirtualLoopback(t *testing.T) {
	nis, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, ni := range nis {
		if ni.Flags&net.FlagLoopback == 0 {
			continue
		}
		v, known := osVirtual(ni)
		if !known || !v {
			t.Fatalf("%s: virtual=%v known=%v", ni.Name, v, known)
		}
		return
	}
	t.Skip("no loopback interface listed")
}

func TestHyperVHostAdapterCountsAsPhysical(t *testing.T) {
	for d, want := range map[string]bool{
		"Hyper-V Virtual Ethernet Adapter":       true,
		"Hyper-V Virtual Ethernet Adapter #2":    true,
		"Wintun Userspace Tunnel":                false,
		"TAP-Windows Adapter V9":                 false,
		"Microsoft Wi-Fi Direct Virtual Adapter": false,
	} {
		if got := isHyperVHostAdapter(d); got != want {
			t.Errorf("%q: %v, want %v", d, got, want)
		}
	}
	w := utf16.Encode([]rune("Hyper-V Virtual Ethernet Adapter\x00garbage"))
	if got := wcharString(w); got != "Hyper-V Virtual Ethernet Adapter" {
		t.Errorf("wcharString = %q", got)
	}
}

// The description offset must point at a real string: some adapter on this
// machine has a non-empty description that is valid text.
func TestDescriptionOffset(t *testing.T) {
	nis, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, ni := range nis {
		var row [ifRow2Size / 8]uint64
		b := (*[ifRow2Size]byte)(unsafe.Pointer(&row[0]))
		*(*uint32)(unsafe.Pointer(&b[ifRow2IndexOff])) = uint32(ni.Index)
		if r, _, _ := procGetIfEntry.Call(uintptr(unsafe.Pointer(&row[0]))); r != 0 {
			continue
		}
		d := wcharString((*[ifRow2DescrLen]uint16)(unsafe.Pointer(&b[ifRow2DescrOff]))[:])
		t.Logf("%s: %q hardware=%v", ni.Name, d, b[ifRow2FlagsOff]&ifRow2HardwareIf != 0)
		if d != "" && strings.ToValidUTF8(d, "") == d && strings.IndexFunc(d, func(r rune) bool { return r < 0x20 }) < 0 {
			return
		}
	}
	t.Skip("no adapter description could be read")
}
