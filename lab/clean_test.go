//go:build linux && lab

package lab

import (
	"os"
	"testing"
)

// TestCleanLeftovers removes the namespaces and /etc/netns folders of crashed lab runs (prefix
// obl????-) through the harness' own removeLeftovers. It is the body of `make lab-clean` and
// skips unless LAB_CLEAN=1 and the process is root. It never creates a lab.
func TestCleanLeftovers(t *testing.T) {
	if os.Getenv("LAB_CLEAN") != "1" {
		t.Skip("set LAB_CLEAN=1 (make lab-clean) to remove leftovers of crashed lab runs")
	}
	if os.Geteuid() != 0 {
		t.Skip(`needs root: sudo -E env "PATH=$PATH" make lab-clean`)
	}
	removeLeftovers(t)
}
