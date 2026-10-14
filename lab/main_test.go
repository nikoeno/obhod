//go:build linux && lab

package lab

import (
	"fmt"
	"os"
	"testing"
)

// TestMain writes the release report (report.md in the run dir) when any lab test ran.
func TestMain(m *testing.M) {
	code := m.Run()
	if err := finishRun(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
