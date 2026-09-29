package classify

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
)

// Input describes one probe attempt.
type Input struct {
	Stage      verdict.Stage
	Err        error // nil if the stage succeeded
	RxBytes    int64
	TxBytes    int64
	Elapsed    time.Duration
	Timeout    time.Duration // the deadline that applied
	HTTPStatus int
	Location   string
	CertErr    error
	StubHosts  []string // known ISP stub hosts/IPs (from calibration)
	StallWin   [2]int64 // bulk window in bytes, default 12–36 KB
}

// DefaultStallWin is the bulk stall window used when Input.StallWin is zero.
var DefaultStallWin = [2]int64{12 << 10, 36 << 10}

// Classify returns the verdict for a single probe attempt.
func Classify(in Input) verdict.Verdict {
	if hasLocal(in.Err) {
		return verdict.LocalError
	}
	switch in.Stage {
	case verdict.StageResolve:
		if in.Err != nil {
			return verdict.DNSFail
		}
	case verdict.StageTCP:
		switch {
		case in.Err == nil:
		case isRefused(in.Err), isReset(in.Err):
			return verdict.TCPRefused
		case isTimeout(in.Err):
			return verdict.TCPTimeout
		default:
			return verdict.Unknown
		}
	case verdict.StageTLS:
		return tls(in)
	case verdict.StageVerify:
		if in.CertErr != nil {
			return verdict.TLSMITM
		}
		if in.Err != nil {
			return verdict.Unknown
		}
	case verdict.StageHTTP:
		return http(in)
	case verdict.StageBulk:
		return bulk(in)
	default:
		if in.Err != nil {
			return verdict.Unknown
		}
	}
	return verdict.OK
}

func tls(in Input) verdict.Verdict {
	err := in.Err
	switch {
	case err == nil:
		return verdict.OK
	case isSpoof(err):
		return verdict.TLSSpoof
	case isAlert(err):
		return verdict.TLSAlert
	case isReset(err):
		return verdict.TLSRST
	case isEOF(err):
		// Many stacks report an RST after the ClientHello as a plain EOF.
		if in.TxBytes > 0 {
			return verdict.TLSRST
		}
		return verdict.TLSEOF
	case isTimeout(err):
		return verdict.TLSTimeout
	}
	return verdict.Unknown
}

func http(in Input) verdict.Verdict {
	if in.Err != nil {
		switch {
		case isReset(in.Err), isEOF(in.Err):
			return verdict.HTTPRST
		case isTimeout(in.Err):
			return verdict.ReadTimeout
		}
		return verdict.Unknown
	}
	if in.HTTPStatus >= 300 && in.HTTPStatus < 400 {
		if isStub(in.Location, in.StubHosts) {
			return verdict.HTTPStub
		}
		return verdict.OKRedirect
	}
	return verdict.OK
}

func bulk(in Input) verdict.Verdict {
	if in.Err == nil {
		return verdict.OK
	}
	win := in.StallWin
	if win == [2]int64{} {
		win = DefaultStallWin
	}
	switch {
	case isTimeout(in.Err):
		if n := in.RxBytes + in.TxBytes; n >= win[0] && n <= win[1] {
			return verdict.Stall1620
		}
		return verdict.ReadTimeout
	case isReset(in.Err), isEOF(in.Err):
		return verdict.HTTPRST
	}
	return verdict.Unknown
}

// isStub reports whether the redirect target host is one of the known stub hosts.
func isStub(location string, stubs []string) bool {
	if location == "" || len(stubs) == 0 {
		return false
	}
	u, err := url.Parse(location)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, s := range stubs {
		if host != "" && strings.EqualFold(host, s) {
			return true
		}
	}
	return false
}

func hasLocal(err error) bool {
	var le *verdict.LocalErr
	return err != nil && errors.As(err, &le)
}

func text(err error) string { return strings.ToLower(err.Error()) }

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) ||
		(errors.As(err, &ne) && ne.Timeout()) {
		return true
	}
	t := text(err)
	return strings.Contains(t, "i/o timeout") || strings.Contains(t, "timed out")
}

func isRefused(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	t := text(err)
	return strings.Contains(t, "connection refused") || strings.Contains(t, "actively refused")
}

func isReset(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	t := text(err)
	for _, s := range []string{"connection reset", "forcibly closed", "connection aborted", "broken pipe", "wsarecv", "wsasend"} {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}

func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || (err != nil && strings.HasSuffix(text(err), ": eof"))
}

func isSpoof(err error) bool {
	t := text(err)
	for _, s := range []string{"wrong version number", "record overflow", "decode error", "oversized record",
		"does not look like a tls handshake", "unsupported version", "unexpected message"} {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}

func isAlert(err error) bool { return strings.Contains(text(err), "remote error: tls:") }

// rank orders blocking levels for tie-breaks: more "blocked" is higher.
func rank(v verdict.Verdict) int {
	switch v.DefaultBlocking() {
	case verdict.BlockingConfirmed:
		return 4
	case verdict.BlockingLikely:
		return 3
	case verdict.BlockingUnknown:
		return 2
	case verdict.BlockingNone:
		return 1
	}
	return 0
}

// Majority aggregates repeated attempts: the most frequent non-LOCAL verdict wins;
// ties prefer the more "blocked" verdict; all LOCAL (or no attempts) → LOCAL_ERROR.
func Majority(vs []verdict.Verdict) verdict.Verdict {
	count := map[verdict.Verdict]int{}
	var order []verdict.Verdict
	for _, v := range vs {
		if v == verdict.LocalError {
			continue
		}
		if count[v] == 0 {
			order = append(order, v)
		}
		count[v]++
	}
	if len(order) == 0 {
		return verdict.LocalError
	}
	best := order[0]
	for _, v := range order[1:] {
		if count[v] > count[best] || (count[v] == count[best] && rank(v) > rank(best)) {
			best = v
		}
	}
	return best
}

// Confirm upgrades Blocking using differential evidence: a TLS-level failure whose SNI-swap
// attempt to the same IP succeeded means the IP is reachable and only the SNI is blocked.
func Confirm(v verdict.Verdict, swap *verdict.SNISwapResult) verdict.Blocking {
	if SwapReached(v, swap) {
		switch v {
		case verdict.TLSRST, verdict.TLSTimeout, verdict.TLSEOF, verdict.TLSAlert:
			return verdict.BlockingConfirmed
		}
	}
	return v.DefaultBlocking()
}

// SwapReached reports whether the SNI-swap attempt proved the address reachable: the handshake completed, or the
// server answered with a TLS alert (typically "unrecognized name" for a neutral SNI it does not host). An alert
// proves nothing when the original failure was itself an alert.
func SwapReached(v verdict.Verdict, swap *verdict.SNISwapResult) bool {
	if swap == nil {
		return false
	}
	switch swap.Verdict {
	case verdict.OK, verdict.OKRedirect:
		return true
	case verdict.TLSAlert:
		return v != verdict.TLSAlert
	}
	return false
}
