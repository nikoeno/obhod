package verdict

import "time"

// Stage is the probe stage at which an outcome was observed.
type Stage string

// The probe stages, in pipeline order.
const (
	StageResolve Stage = "resolve"
	StageTCP     Stage = "tcp"
	StageTLS     Stage = "tls"
	StageVerify  Stage = "verify"
	StageHTTP    Stage = "http"
	StageBulk    Stage = "bulk"
	StageBurst   Stage = "burst" // deferred
)

// Verdict is the typed outcome of a probe. The string values are frozen (JSON reports).
type Verdict string

// The verdict enum.
const (
	OK          Verdict = "OK"
	OKRedirect  Verdict = "OK_REDIRECT"
	DNSPoisoned Verdict = "DNS_POISONED"
	DNSFail     Verdict = "DNS_FAIL"
	TCPTimeout  Verdict = "TCP_TIMEOUT"
	TCPRefused  Verdict = "TCP_REFUSED"
	TLSRST      Verdict = "TLS_RST"
	TLSTimeout  Verdict = "TLS_TIMEOUT"
	TLSAlert    Verdict = "TLS_ALERT"
	TLSSpoof    Verdict = "TLS_SPOOF"
	TLSMITM     Verdict = "TLS_MITM"
	TLSEOF      Verdict = "TLS_EOF"
	HTTPStub    Verdict = "HTTP_STUB"
	HTTPRST     Verdict = "HTTP_RST"
	Stall1620   Verdict = "STALL_16_20"
	ReadTimeout Verdict = "READ_TIMEOUT"
	ConnLimited Verdict = "CONN_LIMITED"
	LocalError  Verdict = "LOCAL_ERROR"
	Unknown     Verdict = "UNKNOWN"
)

var defaultBlocking = map[Verdict]Blocking{
	OK:          BlockingNone,
	OKRedirect:  BlockingNone,
	DNSPoisoned: BlockingLikely,
	DNSFail:     BlockingUnknown,
	TCPTimeout:  BlockingLikely,
	TCPRefused:  BlockingLikely,
	TLSRST:      BlockingLikely,
	TLSTimeout:  BlockingLikely,
	TLSAlert:    BlockingLikely,
	TLSSpoof:    BlockingLikely,
	TLSMITM:     BlockingConfirmed,
	TLSEOF:      BlockingLikely,
	HTTPStub:    BlockingConfirmed,
	HTTPRST:     BlockingLikely,
	Stall1620:   BlockingConfirmed,
	ReadTimeout: BlockingUnknown,
	ConnLimited: BlockingUnknown,
	LocalError:  BlockingLocal,
	Unknown:     BlockingUnknown,
}

// Valid reports whether v is a known verdict.
func (v Verdict) Valid() bool {
	_, ok := defaultBlocking[v]
	return ok
}

// DefaultBlocking returns the blocking level implied by v alone (DESIGN.md §5).
func (v Verdict) DefaultBlocking() Blocking {
	if b, ok := defaultBlocking[v]; ok {
		return b
	}
	return BlockingUnknown
}

// Blocking says how sure we are that the network blocks the target. The values are frozen.
type Blocking string

// The blocking levels.
const (
	BlockingNone      Blocking = "none"
	BlockingLikely    Blocking = "likely"
	BlockingConfirmed Blocking = "confirmed"
	BlockingLocal     Blocking = "local_error"
	BlockingUnknown   Blocking = "unknown"
)

// Evidence is the raw material behind a verdict. Durations are encoded as nanoseconds.
type Evidence struct {
	Err         string                  `json:"err,omitempty"` // raw error chain, never empty on failure
	TLSVersion  string                  `json:"tls_version,omitempty"`
	CertSubject string                  `json:"cert_subject,omitempty"`
	CertError   string                  `json:"cert_error,omitempty"`
	HTTPStatus  int                     `json:"http_status,omitempty"`
	Location    string                  `json:"location,omitempty"` // redirect target
	RxBytes     int64                   `json:"rx_bytes,omitempty"`
	TxBytes     int64                   `json:"tx_bytes,omitempty"`
	RTT         time.Duration           `json:"rtt,omitempty"`
	StageTimes  map[Stage]time.Duration `json:"stage_times,omitempty"`
	SNISwap     *SNISwapResult          `json:"sni_swap,omitempty"`
}

// SNISwapResult is the outcome of repeating the handshake with a neutral SNI.
type SNISwapResult struct {
	SNI     string  `json:"sni"`
	Verdict Verdict `json:"verdict"`
}

// LocalErr marks failures caused by our own side (dialer misconfig, no uplink…).
type LocalErr struct{ Err error }

// Error implements error.
func (e *LocalErr) Error() string { return "local: " + e.Err.Error() }

// Unwrap returns the wrapped error.
func (e *LocalErr) Unwrap() error { return e.Err }

// Local wraps err as *LocalErr; nil stays nil.
func Local(err error) error {
	if err == nil {
		return nil
	}
	return &LocalErr{Err: err}
}
