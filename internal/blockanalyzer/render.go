package blockanalyzer

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
)

// render prints the calibration summary, warnings and the result table. All presentation lives here:
// the analyzer only produces typed facts. The output is plain text with stable column widths.
func render(w io.Writer, rep analyzer.Report, verbose bool) {
	fmt.Fprintf(w, "calibration: internet=%s dpi=%s\n", rep.Calibration.Internet, rep.Calibration.DPIPresent)
	if rep.Network.PublicIP.IsValid() {
		n := "network: " + rep.Network.PublicIP.String()
		if a := rep.Network.ASN; a.Number != 0 {
			n += fmt.Sprintf(" AS%d", a.Number)
			if a.Name != "" {
				n += " " + a.Name
			}
		}
		fmt.Fprintln(w, n)
	}
	for _, s := range rep.Calibration.Warnings {
		fmt.Fprintf(w, "warning: %s\n", s)
	}
	for _, s := range rep.Warnings {
		fmt.Fprintf(w, "warning: %s\n", s)
	}
	if len(rep.Results) == 0 {
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	head := "DOMAIN\tVERDICT\tBLOCKING\tSTAGE\tSUGGESTED"
	if verbose {
		head += "\tIP\tEVIDENCE"
	}
	fmt.Fprintln(tw, head)
	for _, r := range rep.Results {
		sug := "-"
		if r.Suggestion != "" {
			sug = string(r.Suggestion)
		}
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s", r.Target.Domain, r.Verdict, r.Blocking, r.Stage, sug)
		if verbose {
			ip := "-"
			if r.IP.IsValid() {
				ip = r.IP.String()
			}
			line += "\t" + ip + "\t" + evidenceSummary(r)
		}
		fmt.Fprintln(tw, line)
	}
	tw.Flush()
}

// evidenceSummary renders the evidence of a result on one line.
func evidenceSummary(r analyzer.ProbeResult) string {
	e := r.Evidence
	var parts []string
	if e.Err != "" {
		parts = append(parts, "err="+oneLine(e.Err))
	}
	if e.CertError != "" {
		parts = append(parts, "cert="+oneLine(e.CertError))
	}
	if e.HTTPStatus != 0 {
		parts = append(parts, fmt.Sprintf("http=%d", e.HTTPStatus))
	}
	if e.Location != "" {
		parts = append(parts, "location="+e.Location)
	}
	if e.TLSVersion != "" {
		parts = append(parts, "tls="+e.TLSVersion)
	}
	if e.RxBytes != 0 || e.TxBytes != 0 {
		parts = append(parts, fmt.Sprintf("rx=%d tx=%d", e.RxBytes, e.TxBytes))
	}
	if e.SNISwap != nil {
		parts = append(parts, fmt.Sprintf("sni-swap(%s)=%s", e.SNISwap.SNI, e.SNISwap.Verdict))
	}
	if r.SuggestionNote != "" {
		parts = append(parts, "note="+r.SuggestionNote)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
