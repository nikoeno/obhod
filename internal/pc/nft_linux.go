//go:build linux

package pc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// ipRuleShow lists the IPv4 policy routing rules (`ip rule show`), read-only
// and allowed without root. A missing ip binary or a failing command is an error.
func ipRuleShow(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "ip", "rule", "show").Output()
	if err != nil {
		return "", fmt.Errorf("ip rule show: %w", err)
	}
	return string(out), nil
}

// nftTableExists lists the "inet splitter" table, read-only. ok is false when
// the table (or the permission to read it) is missing.
func nftTableExists(ctx context.Context) (string, bool, error) {
	out, err := exec.CommandContext(ctx, "nft", "list", "table", "inet", "splitter").Output()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(out), true, nil
	case errors.As(err, &exit):
		return "", false, nil
	}
	return "", false, err
}
