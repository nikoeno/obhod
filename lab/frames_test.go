package lab

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// synthFrame builds an Ethernet/IPv4/TCP frame.
func synthFrame(src, dst [4]byte, sport, dport uint16, flags byte, seq uint32, payload []byte) []byte {
	tcp := make([]byte, 20)
	binary.BigEndian.PutUint16(tcp[0:], sport)
	binary.BigEndian.PutUint16(tcp[2:], dport)
	binary.BigEndian.PutUint32(tcp[4:], seq)
	tcp[12] = 5 << 4
	tcp[13] = flags
	ip := make([]byte, 20)
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:], uint16(20+len(tcp)+len(payload)))
	binary.BigEndian.PutUint16(ip[4:], 77)
	ip[8] = 64
	ip[9] = 6
	copy(ip[12:], src[:])
	copy(ip[16:], dst[:])
	eth := make([]byte, 14)
	binary.BigEndian.PutUint16(eth[12:], 0x0800)
	return append(append(append(eth, ip...), tcp...), payload...)
}

func TestParseFrame(t *testing.T) {
	now := time.Unix(100, 0)
	hello := append([]byte{0x16, 3, 1, 0x01, 0x20}, []byte("....desync-a.test....")...)
	f, ok := parseFrame(synthFrame([4]byte{192, 168, 10, 2}, [4]byte{198, 51, 100, 16}, 43840, 443, 0x18, 1000, hello), now, true, "desync-a.test")
	if !ok {
		t.Fatal("not parsed")
	}
	if f.Src.String() != "192.168.10.2:43840" || f.Dst.String() != "198.51.100.16:443" || f.Flags != "AP" || f.Seq != 1000 ||
		f.Len != len(hello) || f.TTL != 64 || f.ID != 77 || f.TLSLen != 0x120 || f.Needle != "full" || !f.Out {
		t.Errorf("%+v", f)
	}

	// A split SNI: the first segment ends inside the needle, the second starts inside it.
	a, _ := parseFrame(synthFrame([4]byte{1, 1, 1, 1}, [4]byte{2, 2, 2, 2}, 1, 2, 0x10, 1, []byte("\x16\x03\x01\x01\x20..desy")), now, true, "desync-a.test")
	b, _ := parseFrame(synthFrame([4]byte{1, 1, 1, 1}, [4]byte{2, 2, 2, 2}, 1, 2, 0x10, 20, []byte("nc-a.test...")), now, true, "desync-a.test")
	if a.Needle != "head" || b.Needle != "tail" || b.TLSLen != -1 {
		t.Errorf("split needle: %q %q (tls %d)", a.Needle, b.Needle, b.TLSLen)
	}
	if s, _ := parseFrame(synthFrame([4]byte{1, 1, 1, 1}, [4]byte{2, 2, 2, 2}, 1, 2, 0x02, 1, nil), now, false, "x"); s.Flags != "S" || s.Needle != "" || s.Len != 0 {
		t.Errorf("syn: %+v", s)
	}

	// Not decoded: too short, ARP, UDP, a non-first fragment, a VLAN-tagged frame is decoded.
	if _, ok := parseFrame([]byte{1, 2, 3}, now, false, ""); ok {
		t.Error("short frame decoded")
	}
	good := synthFrame([4]byte{1, 1, 1, 1}, [4]byte{2, 2, 2, 2}, 1, 2, 0x10, 1, nil)
	arp := append([]byte(nil), good...)
	binary.BigEndian.PutUint16(arp[12:], 0x0806)
	udp := append([]byte(nil), good...)
	udp[14+9] = 17
	frag := append([]byte(nil), good...)
	binary.BigEndian.PutUint16(frag[14+6:], 0x0010)
	for name, fr := range map[string][]byte{"arp": arp, "udp": udp, "fragment": frag} {
		if _, ok := parseFrame(fr, now, false, ""); ok {
			t.Errorf("%s decoded", name)
		}
	}
	vlan := append(append(append([]byte(nil), good[:12]...), 0x81, 0x00, 0x00, 0x05, 0x08, 0x00), good[14:]...)
	if _, ok := parseFrame(vlan, now, false, ""); !ok {
		t.Error("VLAN frame not decoded")
	}
}

func TestFormatFrames(t *testing.T) {
	now := time.Unix(100, 0)
	var fs []Frame
	for i := 0; i < 5; i++ {
		f, _ := parseFrame(synthFrame([4]byte{1, 1, 1, 1}, [4]byte{2, 2, 2, 2}, 1, 443, 0x10, uint32(i), []byte("\x16\x03\x01\x00\x05")), now.Add(time.Duration(i)*time.Millisecond), i%2 == 0, "")
		fs = append(fs, f)
	}
	out := FormatFrames(fs, 3)
	if !strings.Contains(out, "tls-record-len=5") || !strings.Contains(out, "2 more frames") || strings.Count(out, "\n") != 4 {
		t.Errorf("output:\n%s", out)
	}
	if FormatFrames(nil, 0) != "(no frames)\n" {
		t.Error("empty output")
	}
}

func TestParseIPv4(t *testing.T) {
	eth := synthFrame([4]byte{1, 1, 1, 1}, [4]byte{2, 2, 2, 2}, 5, 443, 0x18, 9, []byte("hello"))
	f, ok := parseIPv4(eth[14:], time.Unix(1, 0), false, "")
	if !ok || f.Dst.String() != "2.2.2.2:443" || f.Len != 5 || f.Seq != 9 {
		t.Errorf("%+v %v", f, ok)
	}
	if _, ok := parseIPv4([]byte{0x60, 0, 0}, time.Unix(1, 0), false, ""); ok {
		t.Error("IPv6 decoded")
	}
}
