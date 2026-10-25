package fakedpi

import (
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Kind names a blocking rule.
type Kind string

// The rule kinds.
const (
	NaiveSNI   Kind = "naive-sni"   // match SNI only in a single, unfragmented ClientHello → RST
	FirstHello Kind = "first-hello" // decide on the first TLS record seen, even if it is a fake one
	Reassemble Kind = "reassemble"  // reassemble split ClientHello → RST (defeats simple split)
	Stall      Kind = "stall"       // drop server→client data after N bytes for matching flows
	IPBlock    Kind = "ip-block"    // drop SYNs to a prefix
	QUICBlock  Kind = "quic-block"  // drop UDP/443
)

// Rule is one blocking rule. Rules are evaluated in order for every packet; the first rule that acts
// decides the packet.
//
// Domains match the SNI as a case-insensitive suffix on a label boundary: "example.com" matches
// "example.com" and "www.example.com", not "badexample.com". An empty Domains list matches nothing.
// Prefix is the server address range for IPBlock and Stall; the zero Prefix matches nothing.
// Bytes is the number of server→client TCP payload bytes a Stall flow may deliver.
type Rule struct {
	Kind    Kind
	Domains []string     // for SNI-based kinds and stall (suffix match)
	Prefix  netip.Prefix // for ip-block / stall
	Bytes   int          // for stall
}

// Verdict is what happens to one packet.
type Verdict string

// The verdicts, from weakest to strongest.
const (
	Accept Verdict = "accept"
	Drop   Verdict = "drop"
	Reset  Verdict = "reset" // drop, and inject RSTs to both ends
)

// Decision is what the engine does with one packet.
type Decision struct {
	Verdict Verdict
	Rule    Kind     // the rule that fired; "" for Accept without a match
	Inject  [][]byte // raw IP packets to send (the RSTs), in order
}

// Flow is one connection as the censor saw it.
type Flow struct {
	Proto    string         // "tcp" | "udp"
	Src, Dst netip.AddrPort // client and server
	SNI      string         // first SNI parsed (single record or reassembled); "" if none
	Hellos   int            // ClientHello records seen (fakes count too)
	Rule     Kind           // the rule that acted on the flow; "" if none
	Verdict  Verdict        // the strongest verdict taken on the flow
	First    time.Time
}

const (
	flowTTL    = 2 * time.Minute // idle time after which a flow is forgotten
	reasmLimit = 16 << 10        // client bytes buffered for Reassemble
)

// Engine is the pure packet engine: it keeps per-flow state and decides on one packet at a time.
// It does no I/O and reads no clock; time comes from the now argument of Process.
// It is safe for concurrent use.
//
// Direction: a flow's client is the sender of a SYN without ACK (or the receiver of a SYN+ACK). For
// a flow first seen mid-stream, the server is the side using port 443; failing that, the side with
// the lower port when it is below 1024; failing that, the destination of the first packet.
//
// Flows are keyed by protocol and both endpoints, so both directions share one flow. A flow idle for
// more than 2 minutes (measured with now) is forgotten; the next packet of the same 5-tuple starts a
// new flow. Seen keeps every flow, forgotten ones included.
//
// Once a flow is reset, every later packet of it is dropped (counted under the rule that reset it).
type Engine struct {
	mu        sync.Mutex
	rules     []Rule
	flows     map[flowKey]*flowState
	log       []*Flow
	stats     map[Kind]int
	lastSweep time.Time
}

// New returns the pure packet engine (any OS, no root).
func New(rules []Rule) *Engine {
	return &Engine{
		rules: normalizeRules(rules),
		flows: map[flowKey]*flowState{},
		stats: map[Kind]int{},
	}
}

// SetRules atomically replaces the rules; existing flows keep their state (a reset flow stays reset,
// a flow whose first ClientHello already passed is not judged again by FirstHello).
func (e *Engine) SetRules(rules []Rule) {
	r := normalizeRules(rules)
	e.mu.Lock()
	e.rules = r
	e.mu.Unlock()
}

// Stats returns how many packets each rule acted on (dropped or reset), as a copy.
func (e *Engine) Stats() map[Kind]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[Kind]int, len(e.stats))
	for k, v := range e.stats {
		out[k] = v
	}
	return out
}

// Seen returns every flow so far, oldest first, as a copy.
func (e *Engine) Seen() []Flow {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Flow, len(e.log))
	for i, f := range e.log {
		out[i] = *f
	}
	return out
}

// Process decides on one raw IPv4 or IPv6 packet (no link header), in either direction. Packets
// that do not parse (truncated, fragments, unknown protocols or IPv6 extension headers) are accepted.
//
// Matching per kind:
//   - NaiveSNI: a client segment that holds a complete ClientHello in one TLS record.
//   - FirstHello: the client segment that starts the flow's first ClientHello record (fakes included,
//     TTL and checksums are never checked); the hello must be complete in that segment, otherwise
//     the flow passes. Later hellos of the flow are ignored.
//   - Reassemble: the client byte stream (in sequence order from the SYN, or from the first data
//     segment when no SYN was seen), up to 16 KiB. Out-of-order segments are placed by sequence
//     number; when segments overlap, the later one's bytes win. After each client segment the
//     contiguous buffer prefix is parsed (TLS records joined into one handshake message) and a
//     complete hello is matched. The buffer is kept and re-checked until the server sends payload,
//     so a hello rewritten by a later segment is judged again: a fake sent before the real bytes is
//     overwritten by them (and the real hello is blocked), a fake sent after them hides the real
//     hello. Buffering stops when the server sends payload or when 16 KiB fill without a complete
//     hello.
//   - Stall: server→client TCP segments with payload, for flows whose SNI matches Domains or whose
//     server is in Prefix. Every server→client payload byte of the flow is counted (dropped ones and
//     retransmits too); once the count exceeds Bytes, server→client data is dropped.
//   - IPBlock: TCP SYN without ACK whose destination is in Prefix.
//   - QUICBlock: UDP with destination port 443.
//
// A Reset drops the triggering packet and injects two RST+ACK packets, first to the client, then to
// the server. The RST to the server carries seq = the client's next sequence number as the server
// expects it (the end of the contiguous client data accepted so far, or the server's latest ACK if
// larger; see track for how rewritten fake bytes are handled) and ack = the server's next sequence number. The RST to the client carries seq = the
// server's next sequence number (end of contiguous server data or the client's latest ACK) and
// ack = the client's next sequence number. Unknown values fall back to the triggering segment's
// seq and ack.
func (e *Engine) Process(pkt []byte, now time.Time) Decision {
	p, ok := parsePacket(pkt)
	if !ok {
		return Decision{Verdict: Accept}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sweep(now)
	f := e.flow(&p, now)
	f.last = now
	fromClient := p.src == f.info.Src
	if f.reset {
		e.stats[f.info.Rule]++
		return Decision{Verdict: Drop, Rule: f.info.Rule}
	}
	var o observation
	if p.proto == protoTCP {
		if fromClient {
			o = f.observeClient(&p)
		} else {
			f.s2c += len(p.payload)
		}
	}
	d := e.evaluate(f, &p, fromClient, &o)
	switch d.Verdict {
	case Reset:
		d.Inject = f.rsts(&p)
		f.reset = true
		f.stopReasm()
	case Accept:
		f.track(&p, fromClient, o.rewrite)
	}
	if d.Verdict != Accept {
		e.stats[d.Rule]++
		if strength(d.Verdict) > strength(f.info.Verdict) {
			f.info.Verdict = d.Verdict
			f.info.Rule = d.Rule
		}
	}
	return d
}

// evaluate runs the rules in order; the first one that acts decides.
func (e *Engine) evaluate(f *flowState, p *packet, fromClient bool, o *observation) Decision {
	for _, r := range e.rules {
		switch r.Kind {
		case NaiveSNI:
			if o.singleOK && matchDomain(r.Domains, o.single) {
				return Decision{Verdict: Reset, Rule: r.Kind}
			}
		case FirstHello:
			if o.first && o.singleOK && matchDomain(r.Domains, o.single) {
				return Decision{Verdict: Reset, Rule: r.Kind}
			}
		case Reassemble:
			if o.reasmOK && matchDomain(r.Domains, o.reasm) {
				return Decision{Verdict: Reset, Rule: r.Kind}
			}
		case Stall:
			if p.proto == protoTCP && !fromClient && len(p.payload) > 0 && f.stallMatch(r) && f.s2c > r.Bytes {
				return Decision{Verdict: Drop, Rule: r.Kind}
			}
		case IPBlock:
			if p.proto == protoTCP && p.flags&(flagSYN|flagACK) == flagSYN && r.Prefix.IsValid() && r.Prefix.Contains(p.dst.Addr()) {
				return Decision{Verdict: Drop, Rule: r.Kind}
			}
		case QUICBlock:
			if p.proto == protoUDP && p.dst.Port() == 443 {
				return Decision{Verdict: Drop, Rule: r.Kind}
			}
		}
	}
	return Decision{Verdict: Accept}
}

// sweep forgets idle flows, at most once per TTL.
func (e *Engine) sweep(now time.Time) {
	if e.lastSweep.IsZero() {
		e.lastSweep = now
		return
	}
	if now.Sub(e.lastSweep) < flowTTL {
		return
	}
	e.lastSweep = now
	for k, f := range e.flows {
		if now.Sub(f.last) > flowTTL {
			delete(e.flows, k)
		}
	}
}

// flow returns the live flow of p, creating it (and its log entry) when needed.
func (e *Engine) flow(p *packet, now time.Time) *flowState {
	k := keyOf(p)
	if f, ok := e.flows[k]; ok {
		if now.Sub(f.last) <= flowTTL {
			return f
		}
		delete(e.flows, k)
	}
	client, server := p.src, p.dst
	if !srcIsClient(p) {
		client, server = server, client
	}
	proto := "udp"
	if p.proto == protoTCP {
		proto = "tcp"
	}
	info := &Flow{Proto: proto, Src: client, Dst: server, Verdict: Accept, First: now}
	f := &flowState{info: info}
	e.flows[k] = f
	e.log = append(e.log, info)
	return f
}

// srcIsClient reports whether the sender of p, the first packet of its flow, is the client.
func srcIsClient(p *packet) bool {
	if p.proto == protoTCP {
		switch p.flags & (flagSYN | flagACK) {
		case flagSYN:
			return true
		case flagSYN | flagACK:
			return false
		}
	}
	sp, dp := p.src.Port(), p.dst.Port()
	switch {
	case dp == 443:
		return true
	case sp == 443:
		return false
	case dp < 1024 && dp <= sp:
		return true
	case sp < 1024 && sp < dp:
		return false
	}
	return true
}

type flowKey struct {
	proto uint8
	a, b  netip.AddrPort // a sorts before b
}

func keyOf(p *packet) flowKey {
	a, b := p.src, p.dst
	if b.Compare(a) < 0 {
		a, b = b, a
	}
	return flowKey{proto: p.proto, a: a, b: b}
}

// flowState is the engine's private state for one live flow.
type flowState struct {
	info   *Flow
	last   time.Time
	reset  bool
	client seqTrack // the client's next sequence number as the server expects it
	server seqTrack // the server's next sequence number as the client expects it
	isn    uint32   // client initial sequence number, when hasISN
	hasISN bool

	firstHello bool // the flow's first ClientHello record has been seen
	reasm      *reassembly
	reasmDone  bool
	s2c        int // server→client TCP payload bytes seen
}

// observation is what one client segment revealed about the ClientHello.
type observation struct {
	single   string // SNI of a ClientHello complete in one record of this segment
	singleOK bool
	first    bool   // this segment starts the flow's first ClientHello record
	reasm    string // SNI of the reassembled ClientHello after this segment
	reasmOK  bool
	rewrite  bool // this segment rewrote buffered client bytes with different content
}

func (f *flowState) observeClient(p *packet) observation {
	var o observation
	if p.flags&flagSYN != 0 {
		f.isn, f.hasISN = p.seq, true
	}
	if len(p.payload) == 0 {
		return o
	}
	if isHelloStart(p.payload) {
		f.info.Hellos++
		if !f.firstHello {
			f.firstHello = true
			o.first = true
		}
		if sni, st := parseHello(p.payload, true); st == helloOK {
			o.single, o.singleOK = sni, true
		}
	}
	if !f.reasmDone {
		if f.reasm == nil {
			base := p.seq
			if f.hasISN {
				base = f.isn + 1
			}
			f.reasm = newReassembly(base)
		}
		wrote, differs := f.reasm.write(p.seq, p.payload)
		o.rewrite = differs
		if wrote && f.reasm.contig > 0 {
			sni, st := parseHello(f.reasm.data[:f.reasm.contig], false)
			if st == helloOK {
				o.reasm, o.reasmOK = sni, true
			} else if f.reasm.contig == reasmLimit {
				f.stopReasm()
			}
		}
	}
	if f.info.SNI == "" {
		if o.singleOK {
			f.info.SNI = o.single
		} else if o.reasmOK {
			f.info.SNI = o.reasm
		}
	}
	return o
}

func (f *flowState) stopReasm() {
	f.reasm = nil
	f.reasmDone = true
}

func (f *flowState) stallMatch(r Rule) bool {
	if r.Prefix.IsValid() && r.Prefix.Contains(f.info.Dst.Addr()) {
		return true
	}
	return matchDomain(r.Domains, f.info.SNI)
}

// track updates sequence state from an accepted packet. A client segment that rewrote buffered
// bytes with different content is taken as the real one: the earlier bytes are assumed to be a fake
// the server never got, so the client's next sequence number becomes the end of this segment.
func (f *flowState) track(p *packet, fromClient, rewrite bool) {
	if p.proto != protoTCP {
		return
	}
	end := p.seq + uint32(len(p.payload))
	if p.flags&flagSYN != 0 {
		end++
	}
	if p.flags&flagFIN != 0 {
		end++
	}
	if fromClient {
		if rewrite {
			f.client.next, f.client.ok = end, true
		} else {
			f.client.advance(p.seq, end)
		}
		if p.flags&flagACK != 0 {
			f.server.acked(p.ack)
		}
		return
	}
	f.server.advance(p.seq, end)
	if p.flags&flagACK != 0 {
		f.client.acked(p.ack)
	}
	if len(p.payload) > 0 {
		f.stopReasm()
	}
}

// rsts builds the RST to the client and the RST to the server for the client segment p.
func (f *flowState) rsts(p *packet) [][]byte {
	cNext, cOK := f.client.next, f.client.ok
	if !cOK {
		cNext, cOK = p.seq, true
	}
	sNext, sOK := f.server.next, f.server.ok
	if !sOK && p.flags&flagACK != 0 {
		sNext, sOK = p.ack, true
	}
	flags := uint8(flagRST | flagACK)
	toClient := buildTCP(f.info.Dst, f.info.Src, sNext, cNext, flags)
	toServerFlags := flags
	if !sOK {
		toServerFlags = flagRST
		sNext = 0
	}
	toServer := buildTCP(f.info.Src, f.info.Dst, cNext, sNext, toServerFlags)
	return [][]byte{toClient, toServer}
}

// seqTrack follows one side's next sequence number.
type seqTrack struct {
	next uint32
	ok   bool
}

// advance moves next over a segment [seq, end) that starts at or before it (contiguous data only).
func (t *seqTrack) advance(seq, end uint32) {
	if !t.ok {
		t.next, t.ok = end, true
		return
	}
	if seqLE(seq, t.next) && seqLT(t.next, end) {
		t.next = end
	}
}

// acked raises next to an acknowledgment number from the other side.
func (t *seqTrack) acked(a uint32) {
	if !t.ok || seqLT(t.next, a) {
		t.next, t.ok = a, true
	}
}

func seqLT(a, b uint32) bool { return int32(a-b) < 0 }
func seqLE(a, b uint32) bool { return int32(a-b) <= 0 }

// reassembly buffers the first reasmLimit bytes of the client stream by sequence offset.
type reassembly struct {
	base   uint32
	data   []byte
	have   []uint64 // bitset of filled offsets
	contig int      // length of the filled prefix
}

func newReassembly(base uint32) *reassembly {
	return &reassembly{base: base, data: make([]byte, reasmLimit), have: make([]uint64, reasmLimit/64)}
}

// write stores p at its offset; later writes overwrite earlier bytes. It reports whether any byte
// landed inside the buffer and whether an already buffered byte changed.
func (r *reassembly) write(seq uint32, p []byte) (wrote, differs bool) {
	off := int64(int32(seq - r.base))
	if off < 0 {
		if -off >= int64(len(p)) {
			return false, false
		}
		p = p[-off:]
		off = 0
	}
	if off >= reasmLimit {
		return false, false
	}
	if n := reasmLimit - int(off); len(p) > n {
		p = p[:n]
	}
	o := int(off)
	for i, c := range p {
		j := o + i
		if r.have[j/64]&(1<<(j%64)) != 0 {
			differs = differs || r.data[j] != c
		} else {
			r.have[j/64] |= 1 << (j % 64)
		}
		r.data[j] = c
	}
	for r.contig < reasmLimit && r.have[r.contig/64]&(1<<(r.contig%64)) != 0 {
		r.contig++
	}
	return len(p) > 0, differs
}

func strength(v Verdict) int {
	switch v {
	case Reset:
		return 2
	case Drop:
		return 1
	}
	return 0
}

func normalizeRules(rules []Rule) []Rule {
	out := make([]Rule, len(rules))
	for i, r := range rules {
		r.Prefix = r.Prefix.Masked()
		if r.Domains != nil {
			ds := make([]string, 0, len(r.Domains))
			for _, d := range r.Domains {
				if d = normalizeName(d); d != "" {
					ds = append(ds, d)
				}
			}
			r.Domains = ds
		}
		out[i] = r
	}
	return out
}

func normalizeName(s string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(s), "."))
}

// matchDomain reports whether sni equals one of domains or is a subdomain of one.
func matchDomain(domains []string, sni string) bool {
	if sni == "" {
		return false
	}
	for _, d := range domains {
		if sni == d || (len(sni) > len(d) && strings.HasSuffix(sni, d) && sni[len(sni)-len(d)-1] == '.') {
			return true
		}
	}
	return false
}
