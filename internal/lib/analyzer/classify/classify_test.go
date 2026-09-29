package classify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
)

func opErr(op string, err error) error {
	return &net.OpError{Op: op, Net: "tcp", Err: os.NewSyscallError(op, err)}
}

func TestClassify(t *testing.T) {
	const (
		rs = verdict.StageResolve
		tc = verdict.StageTCP
		tl = verdict.StageTLS
		vf = verdict.StageVerify
		ht = verdict.StageHTTP
		bk = verdict.StageBulk
	)
	timeout := fmt.Errorf("dial tcp: %w", os.ErrDeadlineExceeded)
	cases := []struct {
		name string
		in   Input
		want verdict.Verdict
	}{
		{"resolve ok", Input{Stage: rs}, verdict.OK},
		{"resolve err", Input{Stage: rs, Err: errors.New("no such host")}, verdict.DNSFail},
		{"tcp ok", Input{Stage: tc}, verdict.OK},
		{"tcp refused", Input{Stage: tc, Err: opErr("connect", syscall.ECONNREFUSED)}, verdict.TCPRefused},
		{"tcp refused windows text", Input{Stage: tc, Err: errors.New("connectex: No connection could be made because the target machine actively refused it.")}, verdict.TCPRefused},
		{"tcp reset", Input{Stage: tc, Err: opErr("read", syscall.ECONNRESET)}, verdict.TCPRefused},
		{"tcp timeout", Input{Stage: tc, Err: timeout}, verdict.TCPTimeout},
		{"tcp ctx deadline", Input{Stage: tc, Err: context.DeadlineExceeded}, verdict.TCPTimeout},
		{"tcp other", Input{Stage: tc, Err: errors.New("network is unreachable")}, verdict.Unknown},
		{"tls reset", Input{Stage: tl, TxBytes: 517, Err: opErr("read", syscall.ECONNRESET)}, verdict.TLSRST},
		{"tls reset windows", Input{Stage: tl, TxBytes: 517, Err: errors.New("wsarecv: An existing connection was forcibly closed by the remote host.")}, verdict.TLSRST},
		{"tls eof after hello is rst", Input{Stage: tl, TxBytes: 517, Err: io.EOF}, verdict.TLSRST},
		{"tls unexpected eof after hello", Input{Stage: tl, TxBytes: 517, Err: fmt.Errorf("x: %w", io.ErrUnexpectedEOF)}, verdict.TLSRST},
		{"tls eof nothing sent", Input{Stage: tl, Err: io.EOF}, verdict.TLSEOF},
		{"tls timeout", Input{Stage: tl, TxBytes: 517, Err: timeout}, verdict.TLSTimeout},
		{"tls alert", Input{Stage: tl, Err: errors.New("remote error: tls: handshake failure")}, verdict.TLSAlert},
		{"tls alert internal", Input{Stage: tl, Err: errors.New("remote error: tls: internal error")}, verdict.TLSAlert},
		{"tls spoof version", Input{Stage: tl, Err: errors.New("tls: first record does not look like a TLS handshake")}, verdict.TLSSpoof},
		{"tls spoof wrong version", Input{Stage: tl, Err: errors.New("tls: received record with wrong version number")}, verdict.TLSSpoof},
		{"tls spoof overflow alert", Input{Stage: tl, Err: errors.New("remote error: tls: record overflow")}, verdict.TLSSpoof},
		{"tls spoof decode", Input{Stage: tl, Err: errors.New("remote error: tls: decode error")}, verdict.TLSSpoof},
		{"tls unknown", Input{Stage: tl, Err: errors.New("weird")}, verdict.Unknown},
		{"verify ok", Input{Stage: vf}, verdict.OK},
		{"verify mitm", Input{Stage: vf, CertErr: errors.New("x509: certificate signed by unknown authority")}, verdict.TLSMITM},
		{"http ok", Input{Stage: ht, HTTPStatus: 200}, verdict.OK},
		{"http 404 is ok", Input{Stage: ht, HTTPStatus: 404}, verdict.OK},
		{"http redirect", Input{Stage: ht, HTTPStatus: 301, Location: "https://www.example.com/"}, verdict.OKRedirect},
		{"http stub", Input{Stage: ht, HTTPStatus: 302, Location: "http://Block.ISP.example/x", StubHosts: []string{"block.isp.example"}}, verdict.HTTPStub},
		{"http rst", Input{Stage: ht, Err: opErr("read", syscall.ECONNRESET)}, verdict.HTTPRST},
		{"http eof", Input{Stage: ht, Err: io.EOF}, verdict.HTTPRST},
		{"http timeout", Input{Stage: ht, Err: timeout}, verdict.ReadTimeout},
		{"bulk ok", Input{Stage: bk, RxBytes: 65536}, verdict.OK},
		{"bulk stall in window", Input{Stage: bk, RxBytes: 16500, Err: timeout}, verdict.Stall1620},
		{"bulk stall counts tx", Input{Stage: bk, RxBytes: 500, TxBytes: 16000, Err: timeout}, verdict.Stall1620},
		{"bulk stall at 50k", Input{Stage: bk, RxBytes: 50 << 10, Err: timeout}, verdict.ReadTimeout},
		{"bulk stall at 2k", Input{Stage: bk, RxBytes: 2 << 10, Err: timeout}, verdict.ReadTimeout},
		{"bulk custom window", Input{Stage: bk, RxBytes: 100, StallWin: [2]int64{50, 200}, Err: timeout}, verdict.Stall1620},
		{"bulk reset", Input{Stage: bk, RxBytes: 17000, Err: opErr("read", syscall.ECONNRESET)}, verdict.HTTPRST},
		{"local anywhere", Input{Stage: tl, Err: fmt.Errorf("wrap: %w", verdict.Local(errors.New("no uplink")))}, verdict.LocalError},
		{"local beats timeout", Input{Stage: tc, Err: verdict.Local(timeout)}, verdict.LocalError},
	}
	for _, c := range cases {
		if got := Classify(c.in); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestMajority(t *testing.T) {
	cases := []struct {
		in   []verdict.Verdict
		want verdict.Verdict
	}{
		{nil, verdict.LocalError},
		{[]verdict.Verdict{verdict.LocalError, verdict.LocalError}, verdict.LocalError},
		{[]verdict.Verdict{verdict.OK, verdict.TLSRST, verdict.TLSRST}, verdict.TLSRST},
		{[]verdict.Verdict{verdict.OK, verdict.TLSRST}, verdict.TLSRST},                // tie → more blocked
		{[]verdict.Verdict{verdict.ReadTimeout, verdict.Stall1620}, verdict.Stall1620}, // confirmed > unknown
		{[]verdict.Verdict{verdict.LocalError, verdict.OK}, verdict.OK},
		{[]verdict.Verdict{verdict.OK, verdict.OK, verdict.TLSRST}, verdict.OK},
	}
	for _, c := range cases {
		if got := Majority(c.in); got != c.want {
			t.Errorf("Majority(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestConfirm(t *testing.T) {
	ok := &verdict.SNISwapResult{SNI: "n.example", Verdict: verdict.OK}
	bad := &verdict.SNISwapResult{SNI: "n.example", Verdict: verdict.TLSRST}
	cases := []struct {
		v    verdict.Verdict
		swap *verdict.SNISwapResult
		want verdict.Blocking
	}{
		{verdict.TLSRST, ok, verdict.BlockingConfirmed},
		{verdict.TLSTimeout, ok, verdict.BlockingConfirmed},
		{verdict.TLSRST, &verdict.SNISwapResult{SNI: "n.example", Verdict: verdict.TLSAlert}, verdict.BlockingConfirmed},
		{verdict.TLSAlert, &verdict.SNISwapResult{SNI: "n.example", Verdict: verdict.TLSAlert}, verdict.BlockingLikely},
		{verdict.TLSRST, bad, verdict.BlockingLikely},
		{verdict.TLSRST, nil, verdict.BlockingLikely},
		{verdict.TCPTimeout, ok, verdict.BlockingLikely},
		{verdict.OK, ok, verdict.BlockingNone},
	}
	for _, c := range cases {
		if got := Confirm(c.v, c.swap); got != c.want {
			t.Errorf("Confirm(%s,%v) = %s, want %s", c.v, c.swap, got, c.want)
		}
	}
}
