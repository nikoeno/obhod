//go:build linux && lab

package lab

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Clash lists the live connections of the splitter's sing-box (GET /connections of the Clash API,
// reached from the client namespace with the bearer secret the app generated into
// <paths.root>/run/sing-box.json). Connections are removed from the list when they close, so call
// it while a transfer is still open. The lab's own secret handling: the secret is a per-run random
// value of the fake lab and only appears in the argv of the curl that asks.
func (s *Splitter) Clash(ctx context.Context) ([]ClashConn, error) {
	cfg, err := os.ReadFile(filepath.Join(s.root, "run", "sing-box.json"))
	if err != nil {
		return nil, fmt.Errorf("lab: %w", err)
	}
	addr, secret, err := parseClashAPI(cfg)
	if err != nil {
		return nil, err
	}
	out, err := s.l.Curl(ctx, "-H", "Authorization: Bearer "+secret, "http://"+addr+"/connections")
	if err != nil {
		return nil, fmt.Errorf("lab: clash api: %w", err)
	}
	return parseClashConnections(out)
}
