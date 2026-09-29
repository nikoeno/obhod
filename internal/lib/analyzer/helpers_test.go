package analyzer

import "github.com/nikoejento/obhod/internal/lib/analyzer/dialer"

// directOptionsForTests dials without binding to an interface (loopback needs no uplink).
func directOptionsForTests() dialer.DirectOptions { return dialer.DirectOptions{} }
