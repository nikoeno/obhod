package pc

import (
	"os"
)

const binary = "splitter"

// Main parses args, runs the app, and returns the exit code.
func Main(args []string) int {
	return newCLI(os.Stdout, os.Stderr).run(args)
}
