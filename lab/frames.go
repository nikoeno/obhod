package lab

// This file has no build tag on purpose: the frame parser and printer of the lab's packet sniffer
// are pure Go, so their unit test runs on any OS without root.

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// Frame is one captured Ethernet/IPv4/TCP frame, reduced to what a routing diagnosis needs.
type Frame struct {
	At       time.Time
	Out      bool // the capture saw it leaving the interface (PACKET_OUTGOING)
	Src, Dst netip.AddrPort
	Flags    string // e.g. "SA", "PA", "R"
	Seq, Ack uint32
	Len      int // TCP payload length
	TTL      uint8
	ID       uint16
	TLSLen   int    // record length when the payload starts like a TLS record (0x16 03), else -1
	Needle   string // "full" | "head" | "tail" | "": the needle is whole in this payload, or only its start ends / its end starts the payload
	Head     string // first payload bytes in hex (at most 8)
}

// parseFrame decodes an Ethernet frame. ok is false for anything that is not IPv4 TCP (VLAN tags
// are skipped; fragments and non-first fragments are not decoded). needle is looked for in the
// payload.
func parseFrame(b []byte, at time.Time, out bool, needle string) (Frame, bool) {
	if len(b) < 14 {
		return Frame{}, false
	}
	et := binary.BigEndian.Uint16(b[12:14])
	b = b[14:]
	for et == 0x8100 || et == 0x88a8 {
		if len(b) < 4 {
			return Frame{}, false
		}
		et = binary.BigEndian.Uint16(b[2:4])
		b = b[4:]
	}
	if et != 0x0800 {
		return Frame{}, false
	}
	return parseIPv4(b, at, out, needle)
}

// parseIPv4 decodes a bare IPv4 packet (a TUN device has no link header); see parseFrame.
func parseIPv4(b []byte, at time.Time, out bool, needle string) (Frame, bool) {
	if len(b) < 20 || b[0]>>4 != 4 {
		return Frame{}, false
	}
	ihl := int(b[0]&0x0f) * 4
	total := int(binary.BigEndian.Uint16(b[2:4]))
	if ihl < 20 || len(b) < ihl || b[9] != 6 || binary.BigEndian.Uint16(b[6:8])&0x1fff != 0 {
		return Frame{}, false
	}
	if total < ihl || total > len(b) {
		total = len(b) // a truncated capture or segmentation offload: use what is there
	}
	ip := b[:ihl]
	t := b[ihl:total]
	if len(t) < 20 {
		return Frame{}, false
	}
	doff := int(t[12]>>4) * 4
	if doff < 20 || len(t) < doff {
		return Frame{}, false
	}
	src, _ := netip.AddrFromSlice(ip[12:16])
	dst, _ := netip.AddrFromSlice(ip[16:20])
	payload := t[doff:]
	f := Frame{
		At:     at,
		Out:    out,
		Src:    netip.AddrPortFrom(src, binary.BigEndian.Uint16(t[0:2])),
		Dst:    netip.AddrPortFrom(dst, binary.BigEndian.Uint16(t[2:4])),
		Flags:  tcpFlags(t[13]),
		Seq:    binary.BigEndian.Uint32(t[4:8]),
		Ack:    binary.BigEndian.Uint32(t[8:12]),
		Len:    len(payload),
		TTL:    ip[8],
		ID:     binary.BigEndian.Uint16(ip[4:6]),
		TLSLen: -1,
	}
	if len(payload) >= 5 && payload[0] == 0x16 && payload[1] == 3 {
		f.TLSLen = int(binary.BigEndian.Uint16(payload[3:5]))
	}
	f.Needle = needleState(payload, needle)
	n := min(len(payload), 8)
	f.Head = fmt.Sprintf("%x", payload[:n])
	return f, true
}

func tcpFlags(b byte) string {
	var s strings.Builder
	for _, fl := range []struct {
		bit byte
		c   byte
	}{{0x02, 'S'}, {0x10, 'A'}, {0x08, 'P'}, {0x01, 'F'}, {0x04, 'R'}, {0x20, 'U'}} {
		if b&fl.bit != 0 {
			s.WriteByte(fl.c)
		}
	}
	return s.String()
}

// needleState reports where needle sits in payload: "full" (whole), "head" (the payload ends with a
// proper prefix of the needle, so the needle spans into the next segment), "tail" (the payload starts
// with a proper suffix of the needle, so it began in the previous segment), or "".
func needleState(payload []byte, needle string) string {
	if needle == "" || len(payload) == 0 {
		return ""
	}
	p := string(payload)
	if strings.Contains(p, needle) {
		return "full"
	}
	for n := min(len(needle)-1, len(p)); n >= 2; n-- {
		if strings.HasSuffix(p, needle[:n]) {
			return "head"
		}
	}
	for n := min(len(needle)-1, len(p)); n >= 2; n-- {
		if strings.HasPrefix(p, needle[len(needle)-n:]) {
			return "tail"
		}
	}
	return ""
}

// FormatFrames prints frames one per line, times relative to the first frame. max <= 0 prints all.
func FormatFrames(frames []Frame, max int) string {
	var b strings.Builder
	if len(frames) == 0 {
		return "(no frames)\n"
	}
	t0 := frames[0].At
	for i, f := range frames {
		if max > 0 && i >= max {
			fmt.Fprintf(&b, "... %d more frames\n", len(frames)-max)
			break
		}
		dir := "in "
		if f.Out {
			dir = "out"
		}
		tls := ""
		if f.TLSLen >= 0 {
			tls = fmt.Sprintf(" tls-record-len=%d", f.TLSLen)
		}
		nd := ""
		if f.Needle != "" {
			nd = " sni:" + f.Needle
		}
		fmt.Fprintf(&b, "%+9.3fms %s %s > %s [%s] seq=%d ack=%d len=%d ttl=%d id=%d head=%s%s%s\n",
			float64(f.At.Sub(t0).Microseconds())/1000, dir, f.Src, f.Dst, f.Flags, f.Seq, f.Ack, f.Len, f.TTL, f.ID, f.Head, tls, nd)
	}
	return b.String()
}
