package netif

import (
	"net"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	iphlpapi       = syscall.NewLazyDLL("iphlpapi.dll")
	procGetIfEntry = iphlpapi.NewProc("GetIfEntry2")
)

// Offsets into MIB_IF_ROW2 (netioapi.h), which is 1352 bytes on every
// Windows architecture: InterfaceIndex follows the 8-byte LUID, Description
// (WCHAR[257]) follows the GUID and Alias (WCHAR[257]), and the
// InterfaceAndOperStatusFlags byte (bit 0: HardwareInterface) follows the
// Alias/Description strings, both physical addresses and six ULONGs.
const (
	ifRow2Size       = 1352
	ifRow2IndexOff   = 8
	ifRow2DescrOff   = 542
	ifRow2DescrLen   = 257 // WCHARs
	ifRow2FlagsOff   = 1152
	ifRow2HardwareIf = 0x01
)

// hyperVHostAdapter is the description prefix of the host's adapter on a
// Hyper-V virtual switch. With an external switch it carries the real uplink
// ("vEthernet (...)"), so it must not count as virtual. On an internal switch
// (WSL, Default Switch) it is never the default route, so it is not chosen.
const hyperVHostAdapter = "Hyper-V Virtual Ethernet Adapter"

// osVirtual asks Windows whether the adapter is backed by hardware. Wintun,
// TAP, Wi-Fi Direct and other software adapters do not set the
// HardwareInterface flag; the Hyper-V host adapter is the one exception
// counted as physical. known is false when the call fails.
func osVirtual(ni net.Interface) (virtual, known bool) {
	if procGetIfEntry.Find() != nil {
		return false, false
	}
	var row [ifRow2Size / 8]uint64 // 8-byte aligned like the C struct
	b := (*[ifRow2Size]byte)(unsafe.Pointer(&row[0]))
	*(*uint32)(unsafe.Pointer(&b[ifRow2IndexOff])) = uint32(ni.Index)
	if r, _, _ := procGetIfEntry.Call(uintptr(unsafe.Pointer(&row[0]))); r != 0 {
		return false, false
	}
	if b[ifRow2FlagsOff]&ifRow2HardwareIf != 0 {
		return false, true
	}
	descr := (*[ifRow2DescrLen]uint16)(unsafe.Pointer(&b[ifRow2DescrOff]))
	return !isHyperVHostAdapter(wcharString(descr[:])), true
}

// wcharString decodes a NUL-terminated UTF-16 buffer.
func wcharString(w []uint16) string {
	for i, c := range w {
		if c == 0 {
			w = w[:i]
			break
		}
	}
	return string(utf16.Decode(w))
}

// isHyperVHostAdapter matches "Hyper-V Virtual Ethernet Adapter" and its
// numbered forms ("... #2").
func isHyperVHostAdapter(description string) bool {
	return strings.HasPrefix(description, hyperVHostAdapter)
}
