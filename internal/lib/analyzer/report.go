package analyzer

import (
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
)

// ReportSchema is the JSON report schema version (DESIGN §8). Any change means a bump and new golden files.
const ReportSchema = 1

// Report is the outcome of a batch run.
type Report struct {
	Schema      int           `json:"schema"`
	Version     string        `json:"version"`
	StartedAt   time.Time     `json:"started_at"`
	Network     NetworkInfo   `json:"network"`
	Calibration Calibration   `json:"calibration"`
	Results     []ProbeResult `json:"results"`
	Warnings    []string      `json:"warnings,omitempty"`
}

// NetworkInfo identifies the network the probes ran from.
type NetworkInfo struct {
	PublicIP netip.Addr `json:"public_ip,omitzero"`
	ASN      ASN        `json:"asn,omitzero"`
}

// EncodeReport writes r as indented JSON (schema 1) with a trailing newline.
func EncodeReport(w io.Writer, r Report) error {
	r.Schema = ReportSchema
	if r.Results == nil {
		r.Results = []ProbeResult{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// DecodeReport reads a JSON report. It rejects other schemas and unknown verdict or blocking values.
func DecodeReport(r io.Reader) (Report, error) {
	var rep Report
	dec := json.NewDecoder(r)
	if err := dec.Decode(&rep); err != nil {
		return Report{}, fmt.Errorf("decode report: %w", err)
	}
	if rep.Schema != ReportSchema {
		return Report{}, fmt.Errorf("decode report: unsupported schema %d (want %d)", rep.Schema, ReportSchema)
	}
	for i, res := range rep.Results {
		if !res.Verdict.Valid() {
			return Report{}, fmt.Errorf("decode report: result %d: unknown verdict %q", i, res.Verdict)
		}
		if !validBlocking(res.Blocking) {
			return Report{}, fmt.Errorf("decode report: result %d: unknown blocking %q", i, res.Blocking)
		}
	}
	return rep, nil
}

func validBlocking(b Blocking) bool {
	switch b {
	case verdict.BlockingNone, verdict.BlockingLikely, verdict.BlockingConfirmed, verdict.BlockingLocal, verdict.BlockingUnknown:
		return true
	}
	return false
}
