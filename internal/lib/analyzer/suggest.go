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
	swapOK := classify.SwapReached(r.Verdict, r.Evidence.SNISwap)
	switch r.Verdict {
	case verdict.OK, verdict.OKRedirect:
		return route.Direct, ""
	case verdict.TLSRST, verdict.TLSTimeout, verdict.TLSSpoof, verdict.TLSAlert, verdict.TLSEOF:
		if swapOK {
			return route.Desync, "only the SNI is blocked"
		}
		return route.Tunnel, "SNI swap did not help: the address itself looks blocked"
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
