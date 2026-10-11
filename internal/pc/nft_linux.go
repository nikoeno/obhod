//go:build linux

package pc

import (
	"context"
	"errors"
	"os/exec"
)

// nftTableExists asks nftables, read-only, whether the "inet splitter" table exists.
func nftTableExists(ctx context.Context) (bool, error) {
	err := exec.CommandContext(ctx, "nft", "list", "table", "inet", "splitter").Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit):
		return false, nil // the table (or the permission to read it) is missing
	}
	return false, err
}
