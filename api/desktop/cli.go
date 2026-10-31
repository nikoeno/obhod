package desktop

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"

	"github.com/nikoejento/obhod/internal/lib/paths"
	"github.com/nikoejento/obhod/internal/lib/version"
	"github.com/nikoejento/obhod/internal/pc"
)

const binary = "splitter"

const usage = `usage: splitter <command> [flags]

commands:
  run [--config PATH] [--parent-pid PID]
  run --ui --token-file PATH [--listen ADDR] [--addr-file PATH] [--config PATH] [--parent-pid PID]
        start the app: checker, desync engine, sing-box, monitor, status page.
        --ui (used by the desktop app) starts disconnected and serves the
        control API: it needs --token-file (the bearer token, one line, owner-only
        on Linux), listens on a loopback address (--listen, default
        127.0.0.1:0), prints "OBHOD-API v1 <host:port>" on stdout once ready and
        writes the address to --addr-file when given. --parent-pid makes the
        process exit when that process does.
  check-config [--config PATH]
        load the config and the secrets, render the sing-box and desync
        configuration once and run "sing-box check" when the binary exists.
  --version
  --help

The default config is <config dir>/app.yaml.

exit codes: 0 ok, 1 config or usage error, 2 needs administrator rights or a
manual setup step (the message says which), 3 runtime failure.
`

// runner is what the CLI needs from an App in terminal mode.
type runner interface {
	Run(ctx context.Context) error
}

// cli holds what tests replace.
type cli struct {
	stdout, stderr io.Writer

	defaultPaths func() (paths.Paths, error)
	newApp       func(cfg pc.Config, p paths.Paths, opts ...pc.Option) (runner, error)
	newEngine    func(cfg pc.Config, p paths.Paths, opts ...pc.Option) (engine, error) // run --ui
	signalCtx    func() (context.Context, context.CancelFunc)
	check        pc.CheckEnv
}

func newCLI(stdout, stderr io.Writer) *cli {
	return &cli{
		stdout:       stdout,
		stderr:       stderr,
		defaultPaths: func() (paths.Paths, error) { return paths.Default(binary) },
		newApp: func(cfg pc.Config, p paths.Paths, opts ...pc.Option) (runner, error) {
			return pc.New(cfg, p, opts...)
		},
		newEngine: func(cfg pc.Config, p paths.Paths, opts ...pc.Option) (engine, error) {
			return pc.New(cfg, p, opts...)
		},
		signalCtx: func() (context.Context, context.CancelFunc) {
			return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		},
		check: pc.DefaultCheckEnv(),
	}
}

func (c *cli) errorf(code int, format string, a ...any) int {
	fmt.Fprintf(c.stderr, binary+": "+format+"\n", a...)
	return code
}

func (c *cli) run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(c.stderr, usage)
		return pc.ExitConfig
	}
	switch args[0] {
	case "--version", "version":
		fmt.Fprintln(c.stdout, version.String(binary))
		return pc.ExitOK
	case "--help", "-h", "help":
		fmt.Fprint(c.stdout, usage)
		return pc.ExitOK
	case "run":
		return c.cmdRun(args[1:])
	case "check-config":
		return c.cmdCheck(args[1:])
	}
	fmt.Fprintf(c.stderr, "%s: unknown command %q\n%s", binary, args[0], usage)
	return pc.ExitConfig
}

// load reads the config (and resolves the directories) for a command.
func (c *cli) load(configPath string) (pc.Config, paths.Paths, error) {
	p, err := c.defaultPaths()
	if err != nil {
		return pc.Config{}, p, fmt.Errorf("directories: %w", err)
	}
	if configPath == "" {
		configPath = filepath.Join(p.Config, "app.yaml")
	}
	cfg, err := pc.LoadConfig(configPath)
	if err != nil {
		return pc.Config{}, p, err
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
	var configPath, tokenFile, listen, addrFile string
	var ui bool
	var parent int
	fs, err := c.flags("run", args, func(fs *flag.FlagSet) {
		fs.StringVar(&configPath, "config", "", "")
		fs.BoolVar(&ui, "ui", false, "")
		fs.IntVar(&parent, "parent-pid", 0, "")
		fs.StringVar(&tokenFile, "token-file", "", "")
		fs.StringVar(&listen, "listen", defaultListen, "")
		fs.StringVar(&addrFile, "addr-file", "", "")
	})
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(c.stdout, usage)
			return pc.ExitOK
		}
		return c.errorf(pc.ExitConfig, "run: %v\n%s", err, usage)
	}
	if parent < 0 {
		return c.errorf(pc.ExitConfig, "run: --parent-pid must be positive")
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if !ui {
		for _, name := range []string{"token-file", "listen", "addr-file"} {
			if given[name] {
				return c.errorf(pc.ExitConfig, "run: --%s needs --ui (the control API is served only with --ui)", name)
			}
		}
	}
	var token string
	if ui {
		if tokenFile == "" {
			return c.errorf(pc.ExitConfig, "run: --ui needs --token-file (the control API requires a token)")
		}
		if err := checkListen(listen); err != nil {
			return c.errorf(pc.ExitConfig, "run: %v", err)
		}
		if token, err = readTokenFile(tokenFile); err != nil {
			return c.errorf(pc.ExitConfig, "run: %v", err)
		}
	}
	cfg, p, err := c.load(configPath)
	if err != nil {
		return c.errorf(pc.ExitConfig, "%v", err)
	}
	var ring *pc.LogRing
	var logOut io.Writer = c.stderr
	opts := []pc.Option{pc.WithUI(ui), pc.WithParentPID(parent)}
	if ui {
		ring = pc.NewLogRing(logRingLines)
		logOut = io.MultiWriter(ring, c.stderr) // the ring first: a broken stderr must not starve it
		opts = append(opts, pc.WithLogRing(ring))
	}
	logger, closeLog, err := pc.NewLogger(cfg.Log, p, logOut)
	if err != nil {
		return c.errorf(pc.ExitConfig, "log: %v", err)
	}
	defer closeLog()
	opts = append(opts, pc.WithLogger(logger))

	if ui {
		eng, err := c.newEngine(cfg, p, opts...)
		if err != nil {
			return c.errorf(pc.ExitCode(err), "%v", err)
		}
		return c.runUI(eng, logger, token, listen, addrFile)
	}
	app, err := c.newApp(cfg, p, opts...)
	if err != nil {
		return c.errorf(pc.ExitCode(err), "%v", err)
	}
	ctx, stop := c.signalCtx()
	defer stop()
	if err := app.Run(ctx); err != nil {
		logger.Error("stopped", "err", err)
		return c.errorf(pc.ExitCode(err), "%v", err)
	}
	return pc.ExitOK
}

// logRingLines is how many of the app's own log lines GET /logs?name=splitter can return.
const logRingLines = 200

// runUI binds the control API, runs the app, prints the ready line once the
// app is ready, and serves until the app ends.
func (c *cli) runUI(eng engine, logger *slog.Logger, token, listen, addrFile string) int {
	// A file left by a crashed run holds a dead address: drop it before binding.
	if addrFile != "" {
		if err := os.Remove(addrFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return c.errorf(pc.ExitConfig, "run: --addr-file: cannot remove the old file: %v", err)
		}
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return c.errorf(pc.ExitConfig, "run: cannot listen on %s: %v", listen, err)
	}
	sig, stop := c.signalCtx()
	defer stop()
	ctx, cancel := context.WithCancel(sig)
	defer cancel()

	srv := newServer(eng, token, version.String(binary), logger, ctx, cancel)
	srv.setAddr(ln.Addr())
	httpSrv := srv.httpServer()

	runDone := make(chan error, 1)
	go func() { runDone <- eng.Run(ctx) }()

	select {
	case <-eng.Ready():
	case err := <-runDone:
		_ = ln.Close()
		if err != nil {
			logger.Error("stopped", "err", err)
			return c.errorf(pc.ExitCode(err), "%v", err)
		}
		return pc.ExitOK
	}

	addr := ln.Addr().String()
	if addrFile != "" {
		if err := writeAddrFile(addrFile, addr); err != nil {
			cancel()
			<-runDone
			_ = ln.Close()
			return c.errorf(pc.ExitConfig, "run: --addr-file: %v", err)
		}
		// Whatever way this function returns (a crash that kills the process
		// excepted), the address is dead: do not leave it behind.
		defer os.Remove(addrFile)
	}
	fmt.Fprintf(c.stdout, "OBHOD-API v1 %s\n", addr)
	logger.Info("control API listening")

	var serveErr atomic.Value
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("control API stopped", "err", err)
			serveErr.Store(err)
			cancel()
		}
	}()

	runErr := <-runDone
	srv.finish(httpSrv)
	<-serveDone
	logger.Info("control API closed")
	if runErr != nil {
		logger.Error("stopped", "err", runErr)
		return c.errorf(pc.ExitCode(runErr), "%v", runErr)
	}
	if e, _ := serveErr.Load().(error); e != nil {
		return c.errorf(pc.ExitRuntime, "control API: %v", e)
	}
	return pc.ExitOK
}

func (c *cli) cmdCheck(args []string) int {
	var configPath string
	if _, err := c.flags("check-config", args, func(fs *flag.FlagSet) { fs.StringVar(&configPath, "config", "", "") }); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(c.stdout, usage)
			return pc.ExitOK
		}
		return c.errorf(pc.ExitConfig, "check-config: %v\n%s", err, usage)
	}
	cfg, p, err := c.load(configPath)
	if err != nil {
		return c.errorf(pc.ExitConfig, "%v", err)
	}
	ctx, stop := c.signalCtx()
	defer stop()
	if err := pc.CheckConfig(ctx, cfg, p, c.check, c.stdout); err != nil {
		return c.errorf(pc.ExitConfig, "%v", err)
	}
	return pc.ExitOK
}
