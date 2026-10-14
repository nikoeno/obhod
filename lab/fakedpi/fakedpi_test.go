package fakedpi

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"net/netip"
	"sync"
	"testing"
	"time"
)

const (
	cli4 = "192.168.1.2:40000"
	srv4 = "203.0.113.10:443"
	cli6 = "[2001:db8::2]:40000"
	srv6 = "[2001:db8:1::10]:443"
)

var blocked = []string{"Example.COM."}

func TestParseHello(t *testing.T) {
	h := clientHello(t, "www.Example.com")
	if sni, st := parseHello(h, true); st != helloOK || sni != "www.example.com" {
		t.Fatalf("got %q %v", sni, st)
	}
	for i := range len(h) {
		if _, st := parseHello(h[:i], false); st != helloShort {
			t.Fatalf("prefix %d: status %v, want short", i, st)
		}
	}
	if _, st := parseHello(h[:len(h)-1], true); st != helloBad {
		t.Fatal("truncated single record must not parse")
	}
	multi := craftHello("multi.example.com", 100, 50)
	if _, st := parseHello(multi, true); st != helloBad {
		t.Fatal("multi-record hello must not parse as one record")
	}
	if sni, st := parseHello(multi, false); st != helloOK || sni != "multi.example.com" {
		t.Fatalf("multi-record: %q %v", sni, st)
	}
	if _, st := parseHello([]byte("GET / HTTP/1.1\r\n"), false); st != helloBad {
		t.Fatal("HTTP must be bad")
	}
	if sni, st := parseHello(clientHello(t, ""), true); st != helloOK || sni != "" {
		t.Fatalf("no SNI: %q %v", sni, st)
	}
}

func TestNaiveSNI(t *testing.T) {
	for _, tc := range []struct {
		name, sni string
		splits    []int // payload offsets to split the hello at
		want      Verdict
	}{
		{"listed", "example.com", nil, Reset},
		{"subdomain", "www.example.com", nil, Reset},
		{"unlisted", "example.org", nil, Accept},
		{"label boundary", "badexample.com", nil, Accept},
		{"split at 1", "example.com", []int{1}, Accept},
		{"split mid SNI", "www.example.com", []int{-1}, Accept},
		{"split in 3", "example.com", []int{2, 100}, Accept},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New([]Rule{{Kind: NaiveSNI, Domains: blocked}})
			k := newConn(t, e, cli4, srv4)
			k.handshake()
			h := clientHello(t, tc.sni)
			var parts [][]byte
			prev := 0
			for _, s := range tc.splits {
				if s < 0 {
					s = bytes.Index(h, []byte(tc.sni)) + 4
				}
				parts = append(parts, h[prev:s])
				prev = s
			}
			parts = append(parts, h[prev:])
			var last Decision
			for i, p := range parts {
				last = k.client(p)
				if i < len(parts)-1 {
					k.must(Accept, last)
				}
			}
			k.must(tc.want, last)
			if tc.want == Reset && (last.Rule != NaiveSNI || len(last.Inject) != 2) {
				t.Fatalf("decision %+v", last)
			}
			if got := e.Seen()[0].SNI; got != tc.sni {
				t.Fatalf("Seen SNI %q, want %q (reassembled for the log)", got, tc.sni)
			}
		})
	}
}

func TestNaiveSNIMultiRecordPasses(t *testing.T) {
	e := New([]Rule{{Kind: NaiveSNI, Domains: blocked}})
	k := newConn(t, e, cli4, srv4)
	k.handshake()
	k.must(Accept, k.client(craftHello("example.com", 50, 40))) // tlsrec-style split in one segment
}

// fakeOf makes a fake copy of a client data packet: TTL 1 and a broken TCP checksum, as nfqws2 sends.
func fakeOf(pkt []byte) []byte {
	pkt[8] = 1
	pkt[36] ^= 0xff
	return pkt
}

func TestFirstHello(t *testing.T) {
	rules := []Rule{{Kind: FirstHello, Domains: blocked}}

	t.Run("fake first lets real pass", func(t *testing.T) {
		e := New(rules)
		k := newConn(t, e, cli4, srv4)
		k.handshake()
		k.must(Accept, k.feed(fakeOf(tcpPacket(k.c, k.s, k.cseq, k.sseq, flagACK|flagPSH, clientHello(t, "www.google.com")))))
		k.must(Accept, k.client(clientHello(t, "example.com")))
		f := e.Seen()[0]
		if f.Hellos != 2 || f.SNI != "www.google.com" || f.Verdict != Accept || f.Rule != "" {
			t.Fatalf("flow %+v", f)
		}
	})
	t.Run("blocked first resets", func(t *testing.T) {
		e := New(rules)
		k := newConn(t, e, cli4, srv4)
		k.handshake()
		d := k.client(clientHello(t, "sub.example.com"))
		k.must(Reset, d)
		if d.Rule != FirstHello {
			t.Fatalf("rule %q", d.Rule)
		}
	})
	t.Run("blocked fake first resets", func(t *testing.T) {
		e := New(rules)
		k := newConn(t, e, cli4, srv4)
		k.handshake()
		k.must(Reset, k.feed(fakeOf(tcpPacket(k.c, k.s, k.cseq, k.sseq, flagACK|flagPSH, clientHello(t, "example.com")))))
	})
	t.Run("later hellos ignored", func(t *testing.T) {
		e := New(rules)
		k := newConn(t, e, cli4, srv4)
		k.handshake()
		k.must(Accept, k.client(clientHello(t, "ok.org")))
		k.must(Accept, k.client(clientHello(t, "example.com")))
	})
}

func TestReassemble(t *testing.T) {
	rules := []Rule{{Kind: Reassemble, Domains: blocked}}
	start := func(t *testing.T) (*Engine, *tconn, []byte) {
		e := New(rules)
		k := newConn(t, e, cli4, srv4)
		k.handshake()
		return e, k, clientHello(t, "www.example.com")
	}
	checkRST := func(t *testing.T, d Decision, cNext uint32) {
		t.Helper()
		if d.Verdict != Reset || d.Rule != Reassemble || len(d.Inject) != 2 {
			t.Fatalf("decision %+v", d)
		}
		if got := parseRST(t, d.Inject[1]).seq; got != cNext {
			t.Fatalf("RST to server seq %d, want %d", got, cNext)
		}
	}

	t.Run("single segment", func(t *testing.T) {
		_, k, h := start(t)
		checkRST(t, k.client(h), clientISN+1)
	})
	t.Run("unlisted passes", func(t *testing.T) {
		_, k, _ := start(t)
		k.must(Accept, k.client(clientHello(t, "example.org")))
	})
	t.Run("split", func(t *testing.T) {
		_, k, h := start(t)
		k.must(Accept, k.client(h[:1]))
		k.must(Accept, k.client(h[1:120]))
		checkRST(t, k.client(h[120:]), clientISN+1+120)
	})
	t.Run("multi-record", func(t *testing.T) {
		_, k, _ := start(t)
		h := craftHello("example.com", 300, 64)
		k.must(Accept, k.client(h[:200]))
		checkRST(t, k.client(h[200:]), clientISN+1+200)
	})
	t.Run("out of order", func(t *testing.T) {
		_, k, h := start(t)
		k.must(Accept, k.clientOff(100, h[100:]))
		checkRST(t, k.clientOff(0, h[:100]), clientISN+1)
	})
	t.Run("overlapping retransmit", func(t *testing.T) {
		_, k, h := start(t)
		k.must(Accept, k.clientOff(0, h[:150]))
		k.must(Accept, k.clientOff(50, h[50:200])) // same bytes again, plus more
		k.must(Accept, k.clientOff(0, h[:200]))
		checkRST(t, k.clientOff(180, h[180:]), clientISN+1+200)
	})
	t.Run("fake before real is overwritten", func(t *testing.T) {
		// A harmless complete fake hello, then the real hello split: the real bytes are written
		// later, so they win and the real hello is blocked.
		e, k, h := start(t)
		k.must(Accept, k.feed(fakeOf(tcpPacket(k.c, k.s, k.cseq, k.sseq, flagACK|flagPSH, clientHello(t, "www.google.com")))))
		k.must(Accept, k.clientOff(0, h[:10])) // rewrites the fake: the client's next seq is now +10
		checkRST(t, k.clientOff(10, h[10:]), clientISN+1+10)
		if f := e.Seen()[0]; f.SNI != "www.google.com" || f.Hellos != 2 || f.Verdict != Reset {
			t.Fatalf("flow %+v", f)
		}
	})
	t.Run("fake after real hides it", func(t *testing.T) {
		// nfqws2 fakedsplit pattern: real part 1, then a fake with different bytes at the same seq,
		// then real part 2. The later (fake) bytes win, the buffer no longer holds a valid hello, and
		// the real hello passes.
		_, k, h := start(t)
		k.must(Accept, k.clientOff(0, h[:100]))
		garbage := bytes.Repeat([]byte{0xa5}, 100)
		k.must(Accept, k.feed(fakeOf(tcpPacket(k.c, k.s, clientISN+1, k.sseq, flagACK|flagPSH, garbage))))
		k.must(Accept, k.clientOff(100, h[100:]))
	})
	t.Run("16 KiB limit", func(t *testing.T) {
		e, k, _ := start(t)
		h := craftHello("example.com", 20000, 1<<14)
		for off := 0; off < len(h); off += 1400 {
			k.must(Accept, k.clientOff(off, h[off:min(off+1400, len(h))]))
		}
		if f := e.Seen()[0]; f.SNI != "" {
			t.Fatalf("SNI %q parsed beyond the limit", f.SNI)
		}
		h = craftHello("example.com", 12000, 1<<14) // under the limit: blocked
		e, k, _ = start(t)
		var d Decision
		for off := 0; off < len(h); off += 1400 {
			d = k.clientOff(off, h[off:min(off+1400, len(h))])
		}
		k.must(Reset, d)
		_ = e
	})
	t.Run("stops after server data", func(t *testing.T) {
		_, k, h := start(t)
		k.must(Accept, k.client(h[:10]))
		k.must(Accept, k.server([]byte("HTTP/1.1 200 OK\r\n")))
		k.must(Accept, k.client(h[10:]))
	})
}

func TestResetPackets(t *testing.T) {
	for _, tc := range []struct{ name, c, s string }{{"ipv4", cli4, srv4}, {"ipv6", cli6, srv6}} {
		t.Run(tc.name, func(t *testing.T) {
			e := New([]Rule{{Kind: Reassemble, Domains: blocked}})
			k := newConn(t, e, tc.c, tc.s)
			k.handshake()
			k.must(Accept, k.client([]byte{22, 3, 1})) // first 3 bytes pass
			k.must(Accept, k.server(nil))              // a pure ACK from the server
			k.must(Accept, k.feed(tcpPacket(k.s, k.c, k.sseq, k.cseq, flagACK, nil)))
			h := clientHello(t, "example.com")
			// Send the full hello again from offset 0 (a retransmit covering the first 3 bytes).
			d := k.clientOff(0, h)
			k.must(Reset, d)
			toClient, toServer := parseRST(t, d.Inject[0]), parseRST(t, d.Inject[1])
			wantC := rst{src: k.s, dst: k.c, seq: serverISN + 1, ack: clientISN + 1 + 3, flags: flagRST | flagACK}
			wantS := rst{src: k.c, dst: k.s, seq: clientISN + 1 + 3, ack: serverISN + 1, flags: flagRST | flagACK}
			if toClient != wantC {
				t.Fatalf("RST to client %+v, want %+v", toClient, wantC)
			}
			if toServer != wantS {
				t.Fatalf("RST to server %+v, want %+v", toServer, wantS)
			}
			// Everything after the reset is dropped, both directions.
			k.must(Drop, k.client([]byte("x")))
			k.must(Drop, k.server([]byte("y")))
			if s := e.Stats(); s[Reassemble] != 3 {
				t.Fatalf("stats %v", s)
			}
		})
	}
}

func TestResetUsesServerData(t *testing.T) {
	// The server has sent data the client has not acknowledged yet: the RST to the client uses the
	// server's next seq from its segments.
	e := New([]Rule{{Kind: NaiveSNI, Domains: blocked}})
	k := newConn(t, e, cli4, srv4)
	k.handshake()
	k.must(Accept, k.server([]byte("banner")))
	d := k.feed(tcpPacket(k.c, k.s, k.cseq, serverISN+1, flagACK|flagPSH, clientHello(t, "example.com"))) // banner not acked yet
	k.must(Reset, d)
	if got := parseRST(t, d.Inject[0]); got.seq != serverISN+1+6 {
		t.Fatalf("RST to client seq %d", got.seq)
	}
}

func TestStall(t *testing.T) {
	for _, tc := range []struct {
		name string
		rule Rule
		sni  string
	}{
		{"domains", Rule{Kind: Stall, Domains: blocked, Bytes: 1500}, "cdn.example.com"},
		{"prefix", Rule{Kind: Stall, Prefix: netip.MustParsePrefix("203.0.113.0/24"), Bytes: 1500}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New([]Rule{tc.rule})
			k := newConn(t, e, cli4, srv4)
			k.handshake()
			if tc.sni != "" {
				k.must(Accept, k.client(clientHello(t, tc.sni)))
			}
			data := make([]byte, 1000)
			k.must(Accept, k.server(data))
			k.must(Accept, k.client(data))
			k.must(Accept, k.feed(tcpPacket(k.s, k.c, k.sseq, k.cseq, flagACK, nil))) // pure ACK
			d := k.server(data)
			k.must(Drop, d)
			if d.Rule != Stall {
				t.Fatalf("rule %q", d.Rule)
			}
			k.must(Drop, k.server(data[:10]))
			k.must(Accept, k.feed(tcpPacket(k.s, k.c, k.sseq, k.cseq, flagACK, nil)))
			k.must(Accept, k.client(data))
			if f := e.Seen()[0]; f.Rule != Stall || f.Verdict != Drop {
				t.Fatalf("flow %+v", f)
			}
			if s := e.Stats(); s[Stall] != 2 {
				t.Fatalf("stats %v", s)
			}
		})
	}
	t.Run("other flows untouched", func(t *testing.T) {
		e := New([]Rule{{Kind: Stall, Domains: blocked, Bytes: 10}})
		k := newConn(t, e, cli4, srv4)
		k.handshake()
		k.must(Accept, k.client(clientHello(t, "example.org")))
		k.must(Accept, k.server(make([]byte, 5000)))
	})
}

func TestIPBlock(t *testing.T) {
	e := New([]Rule{{Kind: IPBlock, Prefix: netip.MustParsePrefix("203.0.113.0/28")}})
	c, s := netip.MustParseAddrPort(cli4), netip.MustParseAddrPort(srv4)
	if d := e.Process(tcpPacket(c, s, 1, 0, flagSYN, nil), t0); d.Verdict != Drop || d.Rule != IPBlock {
		t.Fatalf("SYN: %+v", d)
	}
	for _, pkt := range [][]byte{
		tcpPacket(s, c, 9, 2, flagSYN|flagACK, nil),
		tcpPacket(c, s, 2, 10, flagACK, nil),
		tcpPacket(c, s, 2, 10, flagACK|flagPSH, []byte("data")),
		udpPacket(c, s, []byte("q")),
		tcpPacket(c, netip.MustParseAddrPort("203.0.113.99:443"), 1, 0, flagSYN, nil),
	} {
		if d := e.Process(pkt, t0); d.Verdict != Accept {
			t.Fatalf("%+v", d)
		}
	}
}

func TestQUICBlock(t *testing.T) {
	e := New([]Rule{{Kind: QUICBlock}})
	c, s := netip.MustParseAddrPort(cli6), netip.MustParseAddrPort(srv6)
	if d := e.Process(udpPacket(c, s, []byte{0xc0, 0, 0, 0, 1}), t0); d.Verdict != Drop || d.Rule != QUICBlock {
		t.Fatalf("UDP/443: %+v", d)
	}
	dns := netip.AddrPortFrom(s.Addr(), 53)
	for _, pkt := range [][]byte{
		udpPacket(c, dns, []byte("q")),
		udpPacket(netip.AddrPortFrom(s.Addr(), 443), netip.AddrPortFrom(c.Addr(), 50000), []byte("reply")),
		tcpPacket(c, s, 1, 0, flagSYN, nil),
	} {
		if d := e.Process(pkt, t0); d.Verdict != Accept {
			t.Fatalf("%+v", d)
		}
	}
	seen := e.Seen()
	if len(seen) != 4 || seen[0].Proto != "udp" || seen[0].Verdict != Drop || seen[0].Rule != QUICBlock || seen[3].Proto != "tcp" {
		t.Fatalf("seen %+v", seen)
	}
}

func TestRuleOrder(t *testing.T) {
	e := New([]Rule{
		{Kind: IPBlock, Prefix: netip.MustParsePrefix("198.51.100.0/24")},
		{Kind: FirstHello, Domains: []string{"a.test"}},
		{Kind: NaiveSNI, Domains: []string{"a.test", "b.test"}},
	})
	k := newConn(t, e, cli4, srv4)
	k.handshake()
	if d := k.client(clientHello(t, "a.test")); d.Rule != FirstHello {
		t.Fatalf("rule %q", d.Rule)
	}
	k = newConn(t, e, "192.168.1.2:40001", srv4)
	k.handshake()
	if d := k.client(clientHello(t, "b.test")); d.Rule != NaiveSNI {
		t.Fatalf("rule %q", d.Rule)
	}
}

func TestSeenAndStats(t *testing.T) {
	e := New([]Rule{{Kind: NaiveSNI, Domains: blocked}, {Kind: QUICBlock}})
	a := newConn(t, e, cli4, srv4)
	a.handshake()
	a.must(Accept, a.client(clientHello(t, "ok.org")))
	b := newConn(t, e, "192.168.1.2:40001", "203.0.113.11:443")
	b.now = t0.Add(time.Second)
	b.handshake()
	b.must(Reset, b.client(clientHello(t, "example.com")))
	b.must(Drop, b.server([]byte("late")))
	e.Process(udpPacket(netip.MustParseAddrPort("192.168.1.2:5000"), netip.MustParseAddrPort("203.0.113.12:443"), []byte("x")), t0.Add(2*time.Second))

	seen := e.Seen()
	if len(seen) != 3 {
		t.Fatalf("seen %+v", seen)
	}
	want0 := Flow{Proto: "tcp", Src: a.c, Dst: a.s, SNI: "ok.org", Hellos: 1, Verdict: Accept, First: t0.Add(time.Millisecond)}
	if seen[0] != want0 {
		t.Fatalf("flow 0 %+v, want %+v", seen[0], want0)
	}
	want1 := Flow{Proto: "tcp", Src: b.c, Dst: b.s, SNI: "example.com", Hellos: 1, Rule: NaiveSNI, Verdict: Reset, First: t0.Add(time.Second + time.Millisecond)}
	if seen[1] != want1 {
		t.Fatalf("flow 1 %+v, want %+v", seen[1], want1)
	}
	if seen[2].Proto != "udp" || seen[2].Verdict != Drop || seen[2].Rule != QUICBlock {
		t.Fatalf("flow 2 %+v", seen[2])
	}
	stats := e.Stats()
	if len(stats) != 2 || stats[NaiveSNI] != 2 || stats[QUICBlock] != 1 {
		t.Fatalf("stats %v", stats)
	}
	// Copies: changing them does not change the engine.
	seen[0].SNI = "changed"
	stats[NaiveSNI] = 99
	if e.Seen()[0].SNI != "ok.org" || e.Stats()[NaiveSNI] != 2 {
		t.Fatal("Seen/Stats are not copies")
	}
}

func TestSetRulesLiveFlow(t *testing.T) {
	e := New(nil)
	k := newConn(t, e, cli4, srv4)
	k.handshake()
	k.must(Accept, k.client(clientHello(t, "example.com")))
	e.SetRules([]Rule{{Kind: NaiveSNI, Domains: blocked}, {Kind: FirstHello, Domains: []string{"late.test"}}})
	// FirstHello does not judge the flow again: its first hello already passed.
	k.must(Accept, k.client(clientHello(t, "late.test")))
	k.must(Reset, k.client(clientHello(t, "example.com")))
	e.SetRules(nil)
	k.must(Drop, k.client([]byte("x"))) // still reset
	if seen := e.Seen(); len(seen) != 1 || seen[0].Hellos != 3 || seen[0].SNI != "example.com" {
		t.Fatalf("seen %+v", seen)
	}
}

func TestIdleExpiry(t *testing.T) {
	e := New([]Rule{{Kind: NaiveSNI, Domains: blocked}})
	k := newConn(t, e, cli4, srv4)
	k.handshake()
	k.must(Reset, k.client(clientHello(t, "example.com")))
	k.now = k.now.Add(time.Minute)
	k.must(Drop, k.client([]byte("x"))) // within TTL: still the reset flow
	k.now = k.now.Add(2*time.Minute + time.Second)
	k.must(Accept, k.client([]byte("x"))) // idle too long: a new flow
	seen := e.Seen()
	if len(seen) != 2 || seen[1].Verdict != Accept || seen[1].Src != k.c || !seen[1].First.Equal(k.now) {
		t.Fatalf("seen %+v", seen)
	}
	// The sweep forgets idle flows from other 5-tuples too.
	for i := range 10 {
		c := netip.AddrPortFrom(k.c.Addr(), uint16(50000+i))
		e.Process(tcpPacket(c, k.s, 1, 0, flagSYN, nil), k.now)
	}
	e.Process(tcpPacket(k.c, k.s, 1, 0, flagACK, nil), k.now.Add(10*time.Minute))
	e.mu.Lock()
	n := len(e.flows)
	e.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d live flows after sweep, want 1", n)
	}
}

func TestDirectionMidStream(t *testing.T) {
	e := New(nil)
	c, s := netip.MustParseAddrPort(cli4), netip.MustParseAddrPort(srv4)
	e.Process(tcpPacket(s, c, 7, 8, flagACK, []byte("data")), t0) // server speaks first
	dns := netip.MustParseAddrPort("203.0.113.53:53")
	e.Process(udpPacket(dns, netip.MustParseAddrPort("192.168.1.2:53000"), []byte("a")), t0)
	e.Process(tcpPacket(netip.MustParseAddrPort("10.0.0.1:5000"), netip.MustParseAddrPort("10.0.0.2:6000"), 1, 1, flagACK, nil), t0)
	e.Process(tcpPacket(s, c, serverISN, clientISN+1, flagSYN|flagACK, nil), t0.Add(3*time.Minute))
	seen := e.Seen()
	if seen[0].Src != c || seen[0].Dst != s {
		t.Fatalf("443 flow %+v", seen[0])
	}
	if seen[1].Dst != dns {
		t.Fatalf("53 flow %+v", seen[1])
	}
	if seen[2].Src.Port() != 5000 {
		t.Fatalf("high ports flow %+v", seen[2])
	}
	if seen[3].Src != c {
		t.Fatalf("SYN+ACK flow %+v", seen[3])
	}
}

func TestIPv6(t *testing.T) {
	e := New([]Rule{{Kind: NaiveSNI, Domains: blocked}})
	c, s := netip.MustParseAddrPort(cli6), netip.MustParseAddrPort(srv6)
	hello := clientHello(t, "example.com")
	pkt := tcpPacket(c, s, clientISN+1, serverISN+1, flagACK|flagPSH, hello)

	withExt := func(pkt []byte, ext []byte) []byte {
		out := append([]byte{}, pkt[:40]...)
		out[6] = ext[0]
		ext = append([]byte{}, ext...)
		ext[0] = protoTCP
		out = append(out, ext...)
		out = append(out, pkt[40:]...)
		binary.BigEndian.PutUint16(out[4:], uint16(len(out)-40))
		return out
	}
	// The first byte of ext is the header's own type here; withExt moves it into the IPv6 header.
	hopByHop := []byte{0, 0, 1, 4, 0, 0, 0, 0}
	atomicFrag := []byte{44, 0, 0, 0, 0, 0, 0, 1}
	realFrag := []byte{44, 0, 0, 1, 0, 0, 0, 1} // M flag
	esp := []byte{50, 0, 0, 0, 0, 0, 0, 0}

	for _, tc := range []struct {
		name string
		pkt  []byte
		want Verdict
	}{
		{"plain", pkt, Reset},
		{"hop-by-hop", withExt(pkt, hopByHop), Reset},
		{"atomic fragment", withExt(pkt, atomicFrag), Reset},
		{"fragment", withExt(pkt, realFrag), Accept},
		{"esp", withExt(pkt, esp), Accept},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(e.rules)
			d := e.Process(tc.pkt, t0)
			if d.Verdict != tc.want {
				t.Fatalf("verdict %s", d.Verdict)
			}
			if d.Verdict == Reset {
				r := parseRST(t, d.Inject[0])
				if r.dst != c || r.src != s || r.ack != clientISN+1 || r.seq != serverISN+1 {
					t.Fatalf("RST to client %+v", r)
				}
			}
		})
	}
}

func TestIPv4Options(t *testing.T) {
	e := New([]Rule{{Kind: NaiveSNI, Domains: blocked}})
	c, s := netip.MustParseAddrPort(cli4), netip.MustParseAddrPort(srv4)
	pkt := tcpPacket(c, s, 1, 1, flagACK|flagPSH, clientHello(t, "example.com"))
	opt := append([]byte{}, pkt[:20]...)
	opt[0] = 0x46
	opt = append(opt, 1, 1, 1, 0) // NOP NOP NOP EOL
	opt = append(opt, pkt[20:]...)
	binary.BigEndian.PutUint16(opt[2:], uint16(len(opt)))
	if d := e.Process(opt, t0); d.Verdict != Reset {
		t.Fatalf("with options: %s", d.Verdict)
	}
	frag := append([]byte{}, pkt...)
	frag[6] = 0x20 // MF
	if d := New(e.rules).Process(frag, t0); d.Verdict != Accept {
		t.Fatalf("fragment: %s", d.Verdict)
	}
	padded := append(append([]byte{}, pkt...), 0, 0, 0, 0) // link-layer padding past total length
	if d := New(e.rules).Process(padded, t0); d.Verdict != Reset {
		t.Fatalf("padded: %s", d.Verdict)
	}
}

func allRules() []Rule {
	p := netip.MustParsePrefix("0.0.0.0/0")
	return []Rule{
		{Kind: NaiveSNI, Domains: blocked},
		{Kind: FirstHello, Domains: blocked},
		{Kind: Reassemble, Domains: blocked},
		{Kind: Stall, Domains: blocked, Prefix: p, Bytes: 100},
		{Kind: IPBlock, Prefix: netip.MustParsePrefix("203.0.113.0/24")},
		{Kind: QUICBlock},
	}
}

func TestMalformedNoPanic(t *testing.T) {
	e := New(allRules())
	rng := rand.New(rand.NewPCG(1, 2))
	c, s := netip.MustParseAddrPort(cli4), netip.MustParseAddrPort(srv4)
	c6, s6 := netip.MustParseAddrPort(cli6), netip.MustParseAddrPort(srv6)
	valid := [][]byte{
		tcpPacket(c, s, 1, 1, flagACK|flagPSH, clientHello(t, "example.com")),
		tcpPacket(c6, s6, 1, 1, flagACK|flagPSH, craftHello("example.com", 10, 20)),
		udpPacket(c, s, []byte("quic")),
	}
	now := t0
	for i := range 20000 {
		now = now.Add(time.Millisecond)
		var pkt []byte
		switch i % 3 {
		case 0:
			pkt = make([]byte, rng.IntN(120))
			for j := range pkt {
				pkt[j] = byte(rng.Uint32())
			}
			if len(pkt) > 0 && rng.IntN(2) == 0 {
				pkt[0] = []byte{0x45, 0x60}[rng.IntN(2)]
			}
		case 1:
			v := valid[rng.IntN(len(valid))]
			pkt = append([]byte{}, v[:rng.IntN(len(v)+1)]...)
		default:
			pkt = append([]byte{}, valid[rng.IntN(len(valid))]...)
			for range 1 + rng.IntN(8) {
				pkt[rng.IntN(len(pkt))] = byte(rng.Uint32())
			}
		}
		d := e.Process(pkt, now)
		switch d.Verdict {
		case Accept, Drop, Reset:
		default:
			t.Fatalf("verdict %q", d.Verdict)
		}
	}
	if d := e.Process(nil, now); d.Verdict != Accept {
		t.Fatal("nil packet")
	}
}

func FuzzProcess(f *testing.F) {
	c, s := netip.MustParseAddrPort(cli4), netip.MustParseAddrPort(srv4)
	f.Add(tcpPacket(c, s, 1, 1, flagACK|flagPSH, craftHello("example.com", 0, 30)))
	f.Add(udpPacket(c, s, []byte("x")))
	f.Add([]byte{0x60, 0, 0, 0, 0, 8, 0, 64})
	f.Fuzz(func(t *testing.T, pkt []byte) {
		e := New(allRules())
		e.Process(pkt, t0)
		e.Process(pkt, t0.Add(time.Second))
		parseHello(pkt, false)
		parseHello(pkt, true)
	})
}

func TestConcurrent(t *testing.T) {
	e := New(allRules())
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := netip.AddrPortFrom(netip.MustParseAddr("192.168.1.2"), uint16(40000+g))
			s := netip.MustParseAddrPort("198.51.100.1:443")
			for i := range 200 {
				e.Process(tcpPacket(c, s, uint32(i), 0, flagACK, []byte{byte(i)}), t0.Add(time.Duration(i)*time.Millisecond))
				if i%50 == 0 {
					e.SetRules(allRules())
					e.Seen()
					e.Stats()
				}
			}
		}()
	}
	wg.Wait()
	if len(e.Seen()) != 8 {
		t.Fatalf("%d flows", len(e.Seen()))
	}
}
