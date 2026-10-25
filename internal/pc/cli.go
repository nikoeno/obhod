package pc

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/paths"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/lib/version"
	"github.com/nikoejento/obhod/internal/pc/desync"
	"github.com/nikoejento/obhod/internal/pc/singbox"
)

const usage = `usage: splitter <command> [flags]

commands:
  run [--config PATH] [--ui] [--parent-pid PID]
        start the app: checker, desync engine, sing-box, monitor, status page.
        --ui starts disconnected and waits for a connect request (used by the
        desktop app); --parent-pid makes the process exit when that process does.
  check-config [--config PATH]
        load the config and the secrets, render the sing-box and desync
        configuration once and run "sing-box check" when the binary exists.
  --version
  --help

The default config is <config dir>/app.yaml.

exit codes: 0 ok, 1 config or usage error, 2 needs administrator rights or a
manual setup step (the message says which), 3 runtime failure.
`

// runner is what the CLI needs from an App.
type runner interface {
	Run(ctx context.Context) error
}

// cli holds what tests replace.
type cli struct {
	stdout, stderr io.Writer

	defaultPaths func() (paths.Paths, error)
	newApp       func(cfg Config, p paths.Paths, opts ...Option) (runner, error)
	signalCtx    func() (context.Context, context.CancelFunc)
	check        checkEnv
}

// checkEnv is what check-config needs from the machine.
type checkEnv struct {
	findBinary func(name, configured string) (string, error)
	uplink     func(ctx context.Context) (netif.Interface, error)
	singCheck  func(ctx context.Context, binary, file string) error
}

func newCLI(stdout, stderr io.Writer) *cli {
	exe, _ := os.Executable()
	return &cli{
		stdout:       stdout,
		stderr:       stderr,
		defaultPaths: func() (paths.Paths, error) { return paths.Default(binary) },
		newApp: func(cfg Config, p paths.Paths, opts ...Option) (runner, error) {
			return New(cfg, p, opts...)
		},
		signalCtx: func() (context.Context, context.CancelFunc) {
			return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		},
		check: checkEnv{
			findBinary: func(name, configured string) (string, error) {
				return findBinary(name, configured, exe, fileExists)
			},
			uplink:    netif.Uplink,
			singCheck: singbox.Check,
		},
	}
}

func (c *cli) errorf(code int, format string, a ...any) int {
	fmt.Fprintf(c.stderr, binary+": "+format+"\n", a...)
	return code
}

func (c *cli) run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(c.stderr, usage)
		return exitConfig
	}
	switch args[0] {
	case "--version", "version":
		fmt.Fprintln(c.stdout, version.String(binary))
		return exitOK
	case "--help", "-h", "help":
		fmt.Fprint(c.stdout, usage)
		return exitOK
	case "run":
		return c.cmdRun(args[1:])
	case "check-config":
		return c.cmdCheck(args[1:])
	}
	fmt.Fprintf(c.stderr, "%s: unknown command %q\n%s", binary, args[0], usage)
	return exitConfig
}

// load reads the config (and resolves the directories) for a command.
func (c *cli) load(configPath string) (Config, paths.Paths, error) {
	p, err := c.defaultPaths()
	if err != nil {
		return Config{}, p, fmt.Errorf("directories: %w", err)
	}
	if configPath == "" {
		configPath = filepath.Join(p.Config, "app.yaml")
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return Config{}, p, err
	}
	if cfg.Paths.Root != "" {
		p = paths.FromRoot(cfg.Paths.Root)
	}
	return cfg, p, nil
}

func (c *cli) flags(name string, args []string, setup func(*flag.FlagSet)) (*flag.FlagSet, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	setup(fs)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return fs, nil
}

func (c *cli) cmdRun(args []string) int {
	var configPath string
	var ui bool
	var parent int
	if _, err := c.flags("run", args, func(fs *flag.FlagSet) {
		fs.StringVar(&configPath, "config", "", "")
		fs.BoolVar(&ui, "ui", false, "")
		fs.IntVar(&parent, "parent-pid", 0, "")
	}); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(c.stdout, usage)
			return exitOK
		}
		return c.errorf(exitConfig, "run: %v\n%s", err, usage)
	}
	if parent < 0 {
		return c.errorf(exitConfig, "run: --parent-pid must be positive")
	}
	cfg, p, err := c.load(configPath)
	if err != nil {
		return c.errorf(exitConfig, "%v", err)
	}
	logger, closeLog, err := newLogger(cfg.Log, p, c.stderr)
	if err != nil {
		return c.errorf(exitConfig, "log: %v", err)
	}
	defer closeLog()

	app, err := c.newApp(cfg, p, WithLogger(logger), withUI(ui), withParentPID(parent))
	if err != nil {
		return c.errorf(exitCode(err), "%v", err)
	}
	ctx, stop := c.signalCtx()
	defer stop()
	if err := app.Run(ctx); err != nil {
		logger.Error("stopped", "err", err)
		return c.errorf(exitCode(err), "%v", err)
	}
	return exitOK
}

// newLogger logs to stderr and, rotated by size, to a file in Paths.Logs.
func newLogger(cfg LogConfig, p paths.Paths, stderr io.Writer) (*slog.Logger, func(), error) {
	level, _ := parseLevel(cfg.Level)
	var w io.Writer = stderr
	closeFn := func() {}
	if p.Logs != "" {
		if err := os.MkdirAll(p.Logs, 0o700); err != nil {
			return nil, nil, err
		}
		rot, err := newRotator(filepath.Join(p.Logs, cfg.File), int64(cfg.MaxSizeMB)<<20, cfg.MaxFiles)
		if err != nil {
			return nil, nil, err
		}
		w = io.MultiWriter(stderr, rot)
		closeFn = func() { _ = rot.Close() }
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})), closeFn, nil
}

func (c *cli) cmdCheck(args []string) int {
	var configPath string
	if _, err := c.flags("check-config", args, func(fs *flag.FlagSet) { fs.StringVar(&configPath, "config", "", "") }); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(c.stdout, usage)
			return exitOK
		}
		return c.errorf(exitConfig, "check-config: %v\n%s", err, usage)
	}
	cfg, p, err := c.load(configPath)
	if err != nil {
		return c.errorf(exitConfig, "%v", err)
	}
	ctx, stop := c.signalCtx()
	defer stop()
	if err := c.checkConfig(ctx, cfg, p); err != nil {
		return c.errorf(exitConfig, "%v", err)
	}
	return exitOK
}

// checkConfig renders the sing-box and desync output once for a representative
// table, into a private temp directory, and checks them. Secrets are read but
// never printed.
func (c *cli) checkConfig(ctx context.Context, cfg Config, p paths.Paths) error {
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

	sbBin, sbErr := c.check.findBinary("sing-box", cfg.SingBox.Binary)
	scfg := cfg.SingBox
	scfg.Binary = sbBin
	scfg.ClashSecret = "check-config"
	sb, err := singbox.Generate(scfg, ob, table)
	if err != nil {
		return fmt.Errorf("sing-box config: %w", err)
	}

	// The engine plan needs an uplink; without a network, a stand-in is enough to check the rendering.
	up, uerr := c.check.uplink(ctx)
	if uerr != nil {
		up = netif.Interface{Name: "check0", Index: 1, Addrs: []netip.Prefix{}}
	}
	dcfg := cfg.Desync
	dcfg.ExcludeIPs = excl
	engine := dcfg.Engine
	if engine == "" {
		engine = desync.DefaultEngine()
	}
	dBin, dErr := c.check.findBinary(string(engine), cfg.Desync.Binary)
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
		fmt.Fprintf(c.stdout, "skipped sing-box check: %v\n", sbErr)
	} else if err := c.check.singCheck(ctx, sbBin, file); err != nil {
		return fmt.Errorf("sing-box rejected the generated config: %w", err)
	} else {
		fmt.Fprintln(c.stdout, "sing-box check: ok")
	}
	if dErr != nil {
		fmt.Fprintf(c.stdout, "desync engine binary not found (rendering checked only): %v\n", dErr)
	}
	if uerr != nil {
		fmt.Fprintf(c.stdout, "no uplink interface (a stand-in was used for the desync plan): %v\n", uerr)
	}
	fmt.Fprintf(c.stdout, "desync plan: engine %s, %d arguments, %d setup commands\n", plan.Engine, len(plan.Args), len(plan.Setup))
	fmt.Fprintln(c.stdout, "config ok")
	return nil
}
