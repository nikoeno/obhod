package monitor

import (
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
)

// The verdicts the heuristics report. The analyzer's constants live in a lib
// that this package may not import, so the values are repeated here and a test
// checks them against analyzer.Verdict.Valid.
const (
	verdictTLSRST     analyzer.Verdict = "TLS_RST"
	verdictStall1620  analyzer.Verdict = "STALL_16_20"
	verdictTCPTimeout analyzer.Verdict = "TCP_TIMEOUT"
)

// Thresholds that are not configurable.
const (
	rstMaxUpload   = 4 << 10 // a request this small is a ClientHello-sized upload
	rstMaxDownload = 64      // "about zero": less than any TLS ServerHello
	rstMaxAge      = 2 * time.Second
	timeoutMinAge  = 5 * time.Second  // a connect attempt that long without a byte
	timeoutMaxAge  = 30 * time.Second // older idle connections are not connect failures
)

// obs is what the heuristics know about one connection.
type obs struct {
	network  string        // "tcp" or "udp"
	upload   int64         // total bytes sent
	download int64         // total bytes received
	age      time.Duration // lifetime at the last sighting
	idle     time.Duration // time since the download last grew
}

// detectClosed judges a connection that disappeared from the snapshot.
//
//   - almost nothing came back after a small upload, within 2 s: the
//     handshake was reset (TLS_RST);
//   - not a single byte either way after a connect-timeout-sized wait:
//     the connect never completed (TCP_TIMEOUT).
func detectClosed(o obs) (analyzer.Verdict, bool) {
	if o.network != "tcp" {
		return "", false
	}
	if o.upload > 0 && o.upload <= rstMaxUpload && o.download <= rstMaxDownload && o.age <= rstMaxAge {
		return verdictTLSRST, true
	}
	if o.upload == 0 && o.download == 0 && o.age >= timeoutMinAge && o.age <= timeoutMaxAge {
		return verdictTCPTimeout, true
	}
	return "", false
}

// detectOpen judges a connection that is still open: the download froze
// inside the stall window (the "16-20 KB" cut-off) for at least StallMinAge.
func detectOpen(o obs, cfg Config) (analyzer.Verdict, bool) {
	if o.network != "tcp" {
		return "", false
	}
	if o.download >= cfg.StallMinBytes && o.download <= cfg.StallMaxBytes &&
		o.age >= cfg.StallMinAge && o.idle >= cfg.StallMinAge {
		return verdictStall1620, true
	}
	return "", false
}
