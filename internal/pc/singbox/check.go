package singbox

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// maxCheckOutput bounds the part of the checker's output kept in the error.
const maxCheckOutput = 2048

var (
	ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	// A decode error quotes the offending character of a value (for a bad
	// short_id: "invalid byte: U+0053 'S'"); that is part of a secret.
	quotedRune = regexp.MustCompile(`U\+[0-9A-Fa-f]{4,6}( '[^']*')?`)
)

// Check runs "<binary> check -c <file>" and returns its output on failure.
// The output is cleaned before it goes into the error: colour codes are
// removed, the config path is replaced by "<config>", characters quoted from
// a value are redacted and the text is cut to a bounded length.
func Check(ctx context.Context, binary, file string) error {
	cmd := exec.CommandContext(ctx, binary, "check", "-c", file)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("singbox: check: %w: %s", err, cleanCheckOutput(out.String(), file))
	}
	return nil
}

// cleanCheckOutput makes the output of "sing-box check" safe to log.
func cleanCheckOutput(s, file string) string {
	s = ansiEscape.ReplaceAllString(s, "")
	if file != "" {
		s = strings.ReplaceAll(s, file, "<config>")
	}
	s = quotedRune.ReplaceAllString(s, "<redacted>")
	s = strings.TrimSpace(s)
	if len(s) > maxCheckOutput {
		s = strings.ToValidUTF8(s[:maxCheckOutput], "") + " ...(cut)"
	}
	return s
}

// Command returns how to run sing-box with the given config file.
func Command(cfg Config, file string) (path string, args []string) {
	return cfg.Binary, []string{"run", "-c", file}
}
