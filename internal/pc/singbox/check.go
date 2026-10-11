package singbox

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Check runs "<binary> check -c <file>" and returns its output on failure.
func Check(ctx context.Context, binary, file string) error {
	cmd := exec.CommandContext(ctx, binary, "check", "-c", file)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("singbox: check: %w: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// Command returns how to run sing-box with the given config file.
func Command(cfg Config, file string) (path string, args []string) {
	return cfg.Binary, []string{"run", "-c", file}
}
