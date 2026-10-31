package pc

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/paths"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/desync"
	"github.com/nikoejento/obhod/internal/pc/singbox"
)

// CheckEnv is what CheckConfig needs from the machine; tests replace the fields.
type CheckEnv struct {
	// FindBinary returns the path of the named external program (configured is the config's value, may be empty).
	FindBinary func(name, configured string) (string, error)
	// Uplink returns the physical uplink interface.
	Uplink func(ctx context.Context) (netif.Interface, error)
	// SingCheck runs `sing-box check` on file.
	SingCheck func(ctx context.Context, binary, file string) error
}

// DefaultCheckEnv returns the real CheckEnv: binaries are looked up next to the
// executable and in third_party/bin, the uplink comes from netif, and sing-box
// is run for real.
func DefaultCheckEnv() CheckEnv {
	exe, _ := os.Executable()
	return CheckEnv{
		FindBinary: func(name, configured string) (string, error) {
			return findBinary(name, configured, exe, fileExists)
		},
		Uplink:    netif.Uplink,
		SingCheck: singbox.Check,
	}
}

// CheckConfig renders the sing-box and desync output once for a representative
// table, into a private temp directory, and checks them. Progress lines go to
// out. Secrets are read but never printed.
func CheckConfig(ctx context.Context, cfg Config, p paths.Paths, env CheckEnv, out io.Writer) error {
	ob, err := loadSecrets(cfg.Secrets, p)
	if err != nil {
		return err
	}
	excl := excludeIPs(cfg.Desync.ExcludeIPs, ob.Tunnel)
	if len(excl) == 0 {
		return errNoExclude
	}

	// One entry per route; the domains are reserved names that resolve nowhere.
	es := []route.Entry{
		{Domain: "direct.check.invalid", Route: route.Direct, Source: route.SourceStatic},
		{Domain: "desync.check.invalid", Route: route.Desync, Source: route.SourceStatic},
		{Domain: "tunnel.check.invalid", Route: route.Tunnel, Source: route.SourceStatic},
		{Domain: "block.check.invalid", Route: route.Block, Source: route.SourceStatic},
	}
	if ob.Proxy != nil {
		es = append(es, route.Entry{Domain: "proxy.check.invalid", Route: route.Proxy, Source: route.SourceStatic})
	}
	table, err := route.NewTable("check-config", time.Time{}, es)
	if err != nil {
		return err
	}

	sbBin, sbErr := env.FindBinary("sing-box", cfg.SingBox.Binary)
	scfg := cfg.SingBox
	scfg.Binary = sbBin
	scfg.ClashSecret = "check-config"
	sb, err := singbox.Generate(scfg, ob, table)
	if err != nil {
		return fmt.Errorf("sing-box config: %w", err)
	}

	// The engine plan needs an uplink; without a network, a stand-in is enough to check the rendering.
	up, uerr := env.Uplink(ctx)
	if uerr != nil {
		up = netif.Interface{Name: "check0", Index: 1, Addrs: []netip.Prefix{}}
	}
	dcfg := cfg.Desync
	dcfg.ExcludeIPs = excl
	engine := dcfg.Engine
	if engine == "" {
		engine = desync.DefaultEngine()
	}
	dBin, dErr := env.FindBinary(string(engine), cfg.Desync.Binary)
	if dBin == "" {
		dBin = string(engine) // placeholder: only the rendering is checked
	}
	dcfg.Binary = dBin
	plan, err := desync.Build(dcfg, up, table)
	if err != nil {
		return fmt.Errorf("desync plan: %w", err)
	}

	dir, err := os.MkdirTemp("", "splitter-check-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "sing-box.json")
	if err := os.WriteFile(file, sb, 0o600); err != nil {
		return err
	}
	if sbErr != nil {
		fmt.Fprintf(out, "skipped sing-box check: %v\n", sbErr)
	} else if err := env.SingCheck(ctx, sbBin, file); err != nil {
		return fmt.Errorf("sing-box rejected the generated config: %w", err)
	} else {
		fmt.Fprintln(out, "sing-box check: ok")
	}
	if dErr != nil {
		fmt.Fprintf(out, "desync engine binary not found (rendering checked only): %v\n", dErr)
	}
	if uerr != nil {
		fmt.Fprintf(out, "no uplink interface (a stand-in was used for the desync plan): %v\n", uerr)
	}
	fmt.Fprintf(out, "desync plan: engine %s, %d arguments, %d setup commands\n", plan.Engine, len(plan.Args), len(plan.Setup))
	fmt.Fprintln(out, "config ok")
	return nil
}
