package analyzer

import (
	"github.com/nikoejento/obhod/internal/lib/analyzer/classify"
	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
	"github.com/nikoejento/obhod/internal/lib/route"
)

// SuggestRoute maps a direct-route result to a starting route. It is a hint, not a decision: the real
// decision logic lives in another module. The second result is false when there is nothing sensible to suggest.
//
// Calibration-dependent rules (a failed calibration voids every suggestion, TCP failures need a working
// internet check) are applied by the Runner, which fills ProbeResult.Suggestion.
func SuggestRoute(r ProbeResult) (route.Route, bool) {
	rt, _ := suggest(r)
	return rt, rt != ""
}

// suggest returns the route and an optional note.
func suggest(r ProbeResult) (route.Route, string) {
	switch r.Verdict {
	case verdict.OK, verdict.OKRedirect:
		return route.Direct, ""
	case verdict.TLSRST, verdict.TLSTimeout, verdict.TLSSpoof, verdict.TLSAlert, verdict.TLSEOF:
		// A block at the TLS stage means the connection was made, so the packets can be reshaped on the
		// way: desync is worth a try whatever the SNI swap says (a DPI that also drops the neutral SNI
		// still works on the first packets of the flow). The swap only changes the note.
		if classify.SwapReached(r.Verdict, r.Evidence.SNISwap) {
			return route.Desync, "only the SNI is blocked"
		}
		return route.Desync, "blocked at the TLS stage; the SNI swap did not help, so desync is tried before the tunnel"
	case verdict.HTTPRST:
		// TLS worked and the reset came after the request: the same kind of on-path block.
		return route.Desync, "reset after a good TLS handshake"
	case verdict.TCPTimeout, verdict.TCPRefused, verdict.Stall1620, verdict.TLSMITM, verdict.HTTPStub:
		return route.Tunnel, ""
	case verdict.DNSPoisoned:
		return route.Direct, "needs DoH"
	}
	return "", ""
}

// applySuggestion fills the suggestion of res according to the calibration.
func applySuggestion(res *ProbeResult, cal Calibration) {
	if cal.Internet == CheckFailed {
		return
	}
	if (res.Verdict == verdict.TCPTimeout || res.Verdict == verdict.TCPRefused) && cal.Internet != CheckOK {
		return
	}
	res.Suggestion, res.SuggestionNote = suggest(*res)
}
