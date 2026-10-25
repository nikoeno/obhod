package fakedpi

import (
	"encoding/binary"
	"net/netip"
)

const (
	protoTCP = 6
	protoUDP = 17

	flagFIN = 0x01
	flagSYN = 0x02
	flagRST = 0x04
	flagACK = 0x10

	maxExtHeaders = 8
	rstTTL        = 64
)

// packet is the parsed part of one IP packet the engine needs.
type packet struct {
	proto    uint8 // protoTCP or protoUDP
	src, dst netip.AddrPort
	seq, ack uint32 // TCP only
	flags    uint8  // TCP only
	payload  []byte
}

// parsePacket parses an IPv4 or IPv6 packet carrying TCP or UDP. It never panics; anything it
// cannot handle reports false.
func parsePacket(b []byte) (packet, bool) {
	if len(b) == 0 {
		return packet{}, false
	}
	switch b[0] >> 4 {
	case 4:
		return parseIPv4(b)
	case 6:
		return parseIPv6(b)
	}
	return packet{}, false
}

// parseIPv4 handles options (IHL) and refuses fragments.
func parseIPv4(b []byte) (packet, bool) {
	if len(b) < 20 {
		return packet{}, false
	}
	ihl := int(b[0]&0x0f) * 4
	total := int(binary.BigEndian.Uint16(b[2:4]))
	if ihl < 20 || total < ihl || total > len(b) {
		return packet{}, false
	}
	b = b[:total]
	if binary.BigEndian.Uint16(b[6:8])&0x3fff != 0 { // MF flag or fragment offset
		return packet{}, false
	}
	src := netip.AddrFrom4([4]byte(b[12:16]))
	dst := netip.AddrFrom4([4]byte(b[16:20]))
	return parseL4(b[9], src, dst, b[ihl:])
}

// parseIPv6 skips Hop-by-Hop, Routing, Destination Options, AH and atomic Fragment headers; real
// fragments, ESP, jumbograms and unknown headers report false.
func parseIPv6(b []byte) (packet, bool) {
	if len(b) < 40 {
		return packet{}, false
	}
	plen := int(binary.BigEndian.Uint16(b[4:6]))
	if plen == 0 || 40+plen > len(b) {
		return packet{}, false
	}
	b = b[:40+plen]
	src := netip.AddrFrom16([16]byte(b[8:24]))
	dst := netip.AddrFrom16([16]byte(b[24:40]))
	next, rest := b[6], b[40:]
	for range maxExtHeaders + 1 {
		switch next {
		case protoTCP, protoUDP:
			return parseL4(next, src, dst, rest)
		case 0, 43, 60: // Hop-by-Hop, Routing, Destination Options
			if len(rest) < 8 {
				return packet{}, false
			}
			n := (int(rest[1]) + 1) * 8
			if n > len(rest) {
				return packet{}, false
			}
			next, rest = rest[0], rest[n:]
		case 44: // Fragment: only an atomic fragment (offset 0, no M flag) is parsed
			if len(rest) < 8 || binary.BigEndian.Uint16(rest[2:4])&0xfff9 != 0 {
				return packet{}, false
			}
			next, rest = rest[0], rest[8:]
		case 51: // AH
			if len(rest) < 8 {
				return packet{}, false
			}
			n := (int(rest[1]) + 2) * 4
			if n > len(rest) {
				return packet{}, false
			}
			next, rest = rest[0], rest[n:]
		default:
			return packet{}, false
		}
	}
	return packet{}, false
}

func parseL4(proto uint8, src, dst netip.Addr, b []byte) (packet, bool) {
	switch proto {
	case protoTCP:
		if len(b) < 20 {
			return packet{}, false
		}
		off := int(b[12]>>4) * 4
		if off < 20 || off > len(b) {
			return packet{}, false
		}
		return packet{
			proto:   protoTCP,
			src:     netip.AddrPortFrom(src, binary.BigEndian.Uint16(b[0:2])),
			dst:     netip.AddrPortFrom(dst, binary.BigEndian.Uint16(b[2:4])),
			seq:     binary.BigEndian.Uint32(b[4:8]),
			ack:     binary.BigEndian.Uint32(b[8:12]),
			flags:   b[13],
			payload: b[off:],
		}, true
	case protoUDP:
		if len(b) < 8 {
			return packet{}, false
		}
		payload := b[8:]
		if n := int(binary.BigEndian.Uint16(b[4:6])); n >= 8 && n <= len(b) {
			payload = b[8:n]
		}
		return packet{
			proto:   protoUDP,
			src:     netip.AddrPortFrom(src, binary.BigEndian.Uint16(b[0:2])),
			dst:     netip.AddrPortFrom(dst, binary.BigEndian.Uint16(b[2:4])),
			payload: payload,
		}, true
	}
	return packet{}, false
}

// buildTCP returns an IP packet (IPv4 or IPv6 by the address family of src) with a 20-byte TCP
// header and no payload, with valid checksums.
func buildTCP(src, dst netip.AddrPort, seq, ack uint32, flags uint8) []byte {
	tcp := make([]byte, 20)
	binary.BigEndian.PutUint16(tcp[0:2], src.Port())
	binary.BigEndian.PutUint16(tcp[2:4], dst.Port())
	binary.BigEndian.PutUint32(tcp[4:8], seq)
	if flags&flagACK != 0 {
		binary.BigEndian.PutUint32(tcp[8:12], ack)
	}
	tcp[12] = 5 << 4
	tcp[13] = flags
	sa, da := src.Addr(), dst.Addr()
	binary.BigEndian.PutUint16(tcp[16:18], l4Checksum(sa, da, protoTCP, tcp))
	if sa.Is4() {
		ip := make([]byte, 20, 20+len(tcp))
		ip[0] = 0x45
		binary.BigEndian.PutUint16(ip[2:4], uint16(20+len(tcp)))
		binary.BigEndian.PutUint16(ip[6:8], 0x4000) // DF
		ip[8] = rstTTL
		ip[9] = protoTCP
		s4, d4 := sa.As4(), da.As4()
		copy(ip[12:16], s4[:])
		copy(ip[16:20], d4[:])
		binary.BigEndian.PutUint16(ip[10:12], ^fold(sum16(ip, 0)))
		return append(ip, tcp...)
	}
	ip := make([]byte, 40, 40+len(tcp))
	ip[0] = 0x60
	binary.BigEndian.PutUint16(ip[4:6], uint16(len(tcp)))
	ip[6] = protoTCP
	ip[7] = rstTTL
	s16, d16 := sa.As16(), da.As16()
	copy(ip[8:24], s16[:])
	copy(ip[24:40], d16[:])
	return append(ip, tcp...)
}

// l4Checksum is the TCP/UDP checksum over the pseudo-header and seg (whose checksum field is zero).
func l4Checksum(src, dst netip.Addr, proto uint8, seg []byte) uint16 {
	s := sum16(src.AsSlice(), 0)
	s = sum16(dst.AsSlice(), s)
	s += uint32(proto) + uint32(len(seg))
	return ^fold(sum16(seg, s))
}

// sum16 adds b as big-endian 16-bit words to s (an odd last byte is padded with zero).
func sum16(b []byte, s uint32) uint32 {
	for len(b) >= 2 {
		s += uint32(b[0])<<8 | uint32(b[1])
		b = b[2:]
	}
	if len(b) == 1 {
		s += uint32(b[0]) << 8
	}
	return s
}

func fold(s uint32) uint16 {
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return uint16(s)
}
