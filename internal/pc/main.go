package pc

import (
	"fmt"
	"io"
	"os"

	"github.com/nikoejento/obhod/internal/lib/version"
)

const binary = "splitter"

// Main runs the splitter command line and returns the process exit code.
func Main(args []string) int {
	return run(args, os.Stdout, os.Stderr)
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 {
		switch args[0] {
		case "--version":
			fmt.Fprintln(stdout, version.String(binary))
			return 0
		case "--help", "-h":
			fmt.Fprintf(stdout, "usage: %s [--version] [--help]\n", binary)
			return 0
		}
	}
	fmt.Fprintln(stderr, "not implemented yet")
	return 1
}
