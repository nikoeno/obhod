package blockanalyzer

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/version"
)

const binary = "blockanalyzer"

// Exit codes.
const (
	exitOK          = 0 // report produced
	exitUsage       = 1 // usage or config error, or an interrupted run
	exitCalibration = 2 // calibration failed: the network is down
)

// app holds what tests replace: the runner and the dialer.
type app struct {
	newRunner func(cfg analyzer.Config, deps analyzer.Deps) (analyzer.Runner, error)
	newDialer func() (analyzer.Dialer, error)
}

func realApp() app {
	return app{
		newRunner: analyzer.NewRunner,
		newDialer: func() (analyzer.Dialer, error) {
			return analyzer.NewDirectDialer(analyzer.DirectOptions{BindIface: true})
		},
	}
}

// Main runs the blockanalyzer command line and returns the process exit code.
func Main(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return realApp().run(ctx, args, os.Stdout, os.Stderr)
}

const usage = `usage: blockanalyzer [flags] [domain...]
  -f, --file PATH          domains file (one per line, # comments)
  --stages LIST            tcp,tls,http,bulk (default all)
  --fingerprint NAME       chrome|firefox|safari (default chrome)
  --repeats N              default 2
  --concurrency N          default 8
  --calibrate-only         only check the network, probe no domain
  --json PATH              write the JSON report ("-" = stdout)
  --config PATH            YAML with an analyzer: section
  -v                       show evidence
  --version
`

type options struct {
	file, stages, fingerprint, jsonPath, configPath string
	repeats, concurrency                            int
	calibrateOnly, verbose, showVersion             bool
}

func parseFlags(args []string, stderr io.Writer) (options, []string, error) {
	var o options
	fs := flag.NewFlagSet(binary, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.file, "f", "", "")
	fs.StringVar(&o.file, "file", "", "")
	fs.StringVar(&o.stages, "stages", "", "")
	fs.StringVar(&o.fingerprint, "fingerprint", "", "")
	fs.IntVar(&o.repeats, "repeats", 0, "")
	fs.IntVar(&o.concurrency, "concurrency", 0, "")
	fs.BoolVar(&o.calibrateOnly, "calibrate-only", false, "")
	fs.StringVar(&o.jsonPath, "json", "", "")
	fs.StringVar(&o.configPath, "config", "", "")
	fs.BoolVar(&o.verbose, "v", false, "")
	fs.BoolVar(&o.showVersion, "version", false, "")
	if err := fs.Parse(args); err != nil {
		return o, nil, err
	}
	return o, fs.Args(), nil
}

func (a app) run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, rest, err := parseFlags(args, stderr)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(stdout, usage)
		return exitOK
	case err != nil:
		fmt.Fprintf(stderr, "%s: %v\n%s", binary, err, usage)
		return exitUsage
	case o.showVersion:
		fmt.Fprintln(stdout, version.String(binary))
		return exitOK
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, binary+": "+format+"\n", a...)
		return exitUsage
	}

	cfg, err := loadConfig(o.configPath)
	if err != nil {
		return fail("%v", err)
	}
	if o.stages != "" {
		if cfg.Options.Stages, err = analyzer.ParseStageSet(o.stages); err != nil {
			return fail("--stages: %v", err)
		}
	}
	if o.fingerprint != "" {
		cfg.Options.Fingerprint = o.fingerprint
	}
	if o.repeats != 0 {
		cfg.Options.Repeats = o.repeats
	}
	if o.concurrency != 0 {
		cfg.Concurrency = o.concurrency
	}
	if err := cfg.Validate(); err != nil {
		return fail("%v", err)
	}

	domains := rest
	if o.file != "" {
		more, err := readDomains(o.file)
		if err != nil {
			return fail("%v", err)
		}
		domains = append(domains, more...)
	}
	if len(domains) == 0 && !o.calibrateOnly {
		return fail("no domains given (pass domains, -f FILE or --calibrate-only)")
	}
	var targets []analyzer.Target
	if !o.calibrateOnly {
		for _, d := range domains {
			targets = append(targets, analyzer.Target{Domain: d})
		}
	}

	d, err := a.newDialer()
	if err != nil {
		return fail("cannot set up the direct dialer: %v", err)
	}
	r, err := a.newRunner(cfg, analyzer.Deps{System: analyzer.NewSystemResolver(), Procs: analyzer.SystemProcs()})
	if err != nil {
		return fail("%v", err)
	}
	rep, runErr := r.Run(ctx, analyzer.Batch{Targets: targets, Dialers: []analyzer.Dialer{d}, Options: cfg.Options})
	if runErr != nil && len(rep.Results) == 0 && rep.StartedAt.IsZero() {
		return fail("%v", runErr)
	}

	if o.jsonPath == "-" {
		if err := analyzer.EncodeReport(stdout, rep); err != nil {
			return fail("write report: %v", err)
		}
	} else {
		render(stdout, rep, o.verbose)
		if o.jsonPath != "" {
			if err := writeJSON(o.jsonPath, rep); err != nil {
				return fail("write report: %v", err)
			}
		}
	}
	switch {
	case runErr != nil:
		fmt.Fprintf(stderr, "%s: interrupted: %v (partial report)\n", binary, runErr)
		return exitUsage
	case rep.Calibration.Internet == analyzer.CheckFailed:
		return exitCalibration
	}
	return exitOK
}

func writeJSON(path string, rep analyzer.Report) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := analyzer.EncodeReport(f, rep); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// loadConfig returns the defaults, overlaid with the analyzer: section of the YAML file when given.
func loadConfig(path string) (analyzer.Config, error) {
	cfg := analyzer.DefaultConfig()
	if path == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	doc := struct {
		Analyzer *analyzer.Config `yaml:"analyzer"`
	}{Analyzer: &cfg}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// readDomains reads one domain per line; blank lines and #-comments are skipped.
func readDomains(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}
