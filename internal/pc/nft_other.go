//go:build !linux

package pc

import (
	"context"
	"errors"
)

// nftTableExists is Linux only; other systems have no nftables rules to check.
func nftTableExists(context.Context) (string, bool, error) { return "", true, nil }

// ipRuleShow is Linux only; other systems have no policy routing rules to check.
func ipRuleShow(context.Context) (string, error) { return "", errors.ErrUnsupported }
