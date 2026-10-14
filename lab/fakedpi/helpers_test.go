package fakedpi

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

// captureConn records what a TLS client writes and fails every read, so a client handshake yields
// exactly one real ClientHello.
type captureConn struct{ buf bytes.Buffer }

func (c *captureConn) Read([]byte) (int, error)         { return 0, errors.New("capture only") }
func (c *captureConn) Write(b []byte) (int, error)      { return c.buf.Write(b) }
func (c *captureConn) Close() error                     { return nil }
func (c *captureConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *captureConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *captureConn) SetDeadline(time.Time) error      { return nil }
func (c *captureConn) SetReadDeadline(time.Time) error  { return nil }
func (c *captureConn) SetWriteDeadline(time.Time) error { return nil }

// clientHello returns the ClientHello record crypto/tls sends for sni.
func clientHello(t testing.TB, sni string) []byte {
	t.Helper()
	c := &captureConn{}
	// An empty ServerName needs InsecureSkipVerify; nothing is verified anyway, reads fail first.
	_ = tls.Client(c, &tls.Config{ServerName: sni, InsecureSkipVerify: sni == "", MinVersion: tls.VersionTLS12}).Handshake()
	b := c.buf.Bytes()
	if !isHelloStart(b) || len(b) != 5+int(binary.BigEndian.Uint16(b[3:5])) {
		t.Fatalf("captured %d bytes, not one ClientHello record", len(b))
	}
	return b
}

// craftHello builds a minimal ClientHello with an SNI followed by a padding extension of pad bytes,
// cut into TLS records of at most recSize bytes.
func craftHello(sni string, pad, recSize int) []byte {
	var body []byte
	body = append(body, 3, 3)
	body = append(body, make([]byte, 32)...)
	body = append(body, 0)             // session id
	body = append(body, 0, 2, 0x13, 1) // cipher suites
	body = append(body, 1, 0)          // compression
	var exts []byte
	name := []byte(sni)
	exts = binary.BigEndian.AppendUint16(exts, 0)
	exts = binary.BigEndian.AppendUint16(exts, uint16(len(name)+5))
	exts = binary.BigEndian.AppendUint16(exts, uint16(len(name)+3))
	exts = append(exts, 0)
	exts = binary.BigEndian.AppendUint16(exts, uint16(len(name)))
	exts = append(exts, name...)
	exts = binary.BigEndian.AppendUint16(exts, 21)
	exts = binary.BigEndian.AppendUint16(exts, uint16(pad))
	exts = append(exts, make([]byte, pad)...)
	body = binary.BigEndian.AppendUint16(body, uint16(len(exts)))
	body = append(body, exts...)
	msg := []byte{1, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	msg = append(msg, body...)
	var out []byte
	for len(msg) > 0 {
		n := min(recSize, len(msg))
		out = append(out, 22, 3, 1)
		out = binary.BigEndian.AppendUint16(out, uint16(n))
		out = append(out, msg[:n]...)
		msg = msg[n:]
	}
	return out
}

// csum is an RFC 1071 checksum written independently of the engine's.
func csum(parts ...[]byte) uint16 {
	var all []byte
	for _, p := range parts {
		all = append(all, p...)
	}
	if len(all)%2 == 1 {
		all = append(all, 0)
	}
	var s uint64
	for i := 0; i < len(all); i += 2 {
		s += uint64(binary.BigEndian.Uint16(all[i:]))
	}
	for s > 0xffff {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}

func pseudo(src, dst netip.Addr, proto uint8, n int) []byte {
	var b []byte
	b = append(b, src.AsSlice()...)
	b = append(b, dst.AsSlice()...)
	if src.Is4() {
		return append(b, 0, proto, byte(n>>8), byte(n))
	}
	return append(b, 0, 0, byte(n>>8), byte(n), 0, 0, 0, proto)
}

// ipPacket wraps an L4 segment whose checksum field sits at csumOff.
func ipPacket(src, dst netip.Addr, proto uint8, seg []byte, csumOff int) []byte {
	binary.BigEndian.PutUint16(seg[csumOff:], 0)
	binary.BigEndian.PutUint16(seg[csumOff:], csum(pseudo(src, dst, proto, len(seg)), seg))
	if src.Is4() {
		h := make([]byte, 20)
		h[0] = 0x45
		binary.BigEndian.PutUint16(h[2:], uint16(20+len(seg)))
		h[8] = 64
		h[9] = proto
		copy(h[12:], src.AsSlice())
		copy(h[16:], dst.AsSlice())
		binary.BigEndian.PutUint16(h[10:], csum(h))
		return append(h, seg...)
	}
	h := make([]byte, 40)
	h[0] = 0x60
	binary.BigEndian.PutUint16(h[4:], uint16(len(seg)))
	h[6] = proto
	h[7] = 64
	copy(h[8:], src.AsSlice())
	copy(h[24:], dst.AsSlice())
	return append(h, seg...)
}

func tcpPacket(src, dst netip.AddrPort, seq, ack uint32, flags uint8, payload []byte) []byte {
	seg := make([]byte, 20, 20+len(payload))
	binary.BigEndian.PutUint16(seg[0:], src.Port())
	binary.BigEndian.PutUint16(seg[2:], dst.Port())
	binary.BigEndian.PutUint32(seg[4:], seq)
	binary.BigEndian.PutUint32(seg[8:], ack)
	seg[12] = 5 << 4
	seg[13] = flags
	binary.BigEndian.PutUint16(seg[14:], 65535)
	seg = append(seg, payload...)
	return ipPacket(src.Addr(), dst.Addr(), protoTCP, seg, 16)
}

func udpPacket(src, dst netip.AddrPort, payload []byte) []byte {
	seg := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint16(seg[0:], src.Port())
	binary.BigEndian.PutUint16(seg[2:], dst.Port())
	binary.BigEndian.PutUint16(seg[4:], uint16(8+len(payload)))
	seg = append(seg, payload...)
	return ipPacket(src.Addr(), dst.Addr(), protoUDP, seg, 6)
}

var t0 = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

const (
	clientISN = 1000
	serverISN = 5000
	flagPSH   = 0x08
)

// tconn drives one TCP connection through an engine.
type tconn struct {
	t          *testing.T
	e          *Engine
	c, s       netip.AddrPort
	cseq, sseq uint32 // next sequence numbers
	now        time.Time
}

func newConn(t *testing.T, e *Engine, client, server string) *tconn {
	return &tconn{t: t, e: e, c: netip.MustParseAddrPort(client), s: netip.MustParseAddrPort(server), now: t0}
}

func (k *tconn) feed(pkt []byte) Decision {
	k.now = k.now.Add(time.Millisecond)
	return k.e.Process(pkt, k.now)
}

// handshake runs SYN, SYN+ACK, ACK and requires all three to pass.
func (k *tconn) handshake() {
	k.t.Helper()
	k.must(Accept, k.feed(tcpPacket(k.c, k.s, clientISN, 0, flagSYN, nil)))
	k.must(Accept, k.feed(tcpPacket(k.s, k.c, serverISN, clientISN+1, flagSYN|flagACK, nil)))
	k.cseq, k.sseq = clientISN+1, serverISN+1
	k.must(Accept, k.feed(tcpPacket(k.c, k.s, k.cseq, k.sseq, flagACK, nil)))
}

// client sends payload at the client's next seq and advances it.
func (k *tconn) client(payload []byte) Decision {
	d := k.clientAt(k.cseq, payload)
	k.cseq += uint32(len(payload))
	return d
}

// clientAt sends payload at offset off from the first client data byte, without advancing.
func (k *tconn) clientOff(off int, payload []byte) Decision {
	return k.clientAt(clientISN+1+uint32(off), payload)
}

func (k *tconn) clientAt(seq uint32, payload []byte) Decision {
	return k.feed(tcpPacket(k.c, k.s, seq, k.sseq, flagACK|flagPSH, payload))
}

func (k *tconn) server(payload []byte) Decision {
	d := k.feed(tcpPacket(k.s, k.c, k.sseq, k.cseq, flagACK|flagPSH, payload))
	k.sseq += uint32(len(payload))
	return d
}

func (k *tconn) must(want Verdict, d Decision) {
	k.t.Helper()
	if d.Verdict != want {
		k.t.Fatalf("verdict %s (rule %q), want %s", d.Verdict, d.Rule, want)
	}
}

// rst is a parsed injected packet.
type rst struct {
	src, dst netip.AddrPort
	seq, ack uint32
	flags    uint8
}

// parseRST parses an injected packet and checks its IP and TCP checksums.
func parseRST(t *testing.T, b []byte) rst {
	t.Helper()
	var src, dst netip.Addr
	var seg []byte
	switch b[0] >> 4 {
	case 4:
		if csum(b[:20]) != 0 {
			t.Fatal("bad IPv4 header checksum")
		}
		if int(binary.BigEndian.Uint16(b[2:])) != len(b) || b[9] != protoTCP || b[8] == 0 {
			t.Fatalf("bad IPv4 header % x", b[:20])
		}
		src, dst = netip.AddrFrom4([4]byte(b[12:16])), netip.AddrFrom4([4]byte(b[16:20]))
		seg = b[20:]
	case 6:
		if int(binary.BigEndian.Uint16(b[4:])) != len(b)-40 || b[6] != protoTCP || b[7] == 0 {
			t.Fatalf("bad IPv6 header % x", b[:40])
		}
		src, dst = netip.AddrFrom16([16]byte(b[8:24])), netip.AddrFrom16([16]byte(b[24:40]))
		seg = b[40:]
	default:
		t.Fatalf("not an IP packet: % x", b)
	}
	if len(seg) != 20 || seg[12]>>4 != 5 {
		t.Fatalf("bad TCP segment % x", seg)
	}
	if csum(pseudo(src, dst, protoTCP, len(seg)), seg) != 0 {
		t.Fatal("bad TCP checksum")
	}
	return rst{
		src:   netip.AddrPortFrom(src, binary.BigEndian.Uint16(seg[0:])),
		dst:   netip.AddrPortFrom(dst, binary.BigEndian.Uint16(seg[2:])),
		seq:   binary.BigEndian.Uint32(seg[4:]),
		ack:   binary.BigEndian.Uint32(seg[8:]),
		flags: seg[13],
	}
}
