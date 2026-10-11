//go:build !linux

package pc

import "context"

// nftTableExists is Linux only; other systems have no nftables rules to check.
func nftTableExists(context.Context) (bool, error) { return true, nil }
