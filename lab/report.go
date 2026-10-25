//go:build linux && lab

package lab

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// runState is the one release report of a test process: every Up of the process writes its logs
// below the same run dir, and TestMain writes report.md at the end (finishRun). It is the only
// package-level state of the lab, because the report spans tests and the testing API has no other
// place for it.
type runState struct {
	mu       sync.Mutex
	dir      string // created by the first Up that passed Preflight
	version  string
	started  time.Time
	singBox  string // `sing-box version`, first line
	results  []result
	buildDir string            // Binary builds, removed by finishRun
	built    map[string]string // name → built path
	active   map[string]bool   // prefixes of labs of this process that are up
}

func (r *runState) setActive(prefix string, on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		r.active = map[string]bool{}
	}
	if on {
		r.active[prefix] = true
	} else {
		delete(r.active, prefix)
	}
}

func (r *runState) isActive(prefix string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[prefix]
}

type result struct {
	test, scenario, note string
	pass                 bool
	at                   time.Time
}

var labRun runState

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// runDir returns the run dir, creating it on first use:
// $LAB_REPORT_DIR (default <repo>/dist/e2e) /<version>-<YYYYMMDD-HHMMSS>.
func (r *runState) runDir() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dir != "" {
		return r.dir, nil
	}
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	base := os.Getenv("LAB_REPORT_DIR")
	if base == "" {
		base = filepath.Join(root, "dist", "e2e")
	}
	r.started = time.Now()
	r.version = gitVersion(root)
	dir := filepath.Join(base, unsafeName.ReplaceAllString(r.version, "_")+"-"+r.started.Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("lab: run dir: %w", err)
	}
	r.dir = dir
	return dir, nil
}

// gitVersion is `git describe --always --dirty` of the repo, or "dev".
func gitVersion(root string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// safe.directory: under sudo the repo belongs to another user, and git refuses it otherwise.
	cmd := exec.CommandContext(ctx, "git", "-c", "safe.directory="+root, "describe", "--always", "--dirty")
	cmd.Dir = root
	out, err := cmd.Output()
	if v := strings.TrimSpace(string(out)); err == nil && v != "" {
		return v
	}
	return "dev"
}

func (r *runState) setSingBox(v string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.singBox == "" {
		r.singBox = v
	}
}

func (r *runState) add(res result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, res)
}

// finishRun writes report.md when a lab test ran in this process and removes the Binary build dir.
// TestMain calls it after m.Run.
func finishRun() error {
	labRun.mu.Lock()
	defer labRun.mu.Unlock()
	var errs []error
	if labRun.buildDir != "" {
		if err := os.RemoveAll(labRun.buildDir); err != nil { // our own os.MkdirTemp dir
			errs = append(errs, err)
		}
		labRun.buildDir, labRun.built = "", nil
	}
	if labRun.dir != "" {
		if err := os.WriteFile(filepath.Join(labRun.dir, "report.md"), []byte(labRun.markdown()), 0o644); err != nil {
			errs = append(errs, err)
		} else {
			fmt.Fprintf(os.Stderr, "lab: report written to %s\n", filepath.Join(labRun.dir, "report.md"))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("lab: finish run: %v", errs)
	}
	return nil
}

func (r *runState) markdown() string {
	var b strings.Builder
	kernel, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	pass, fail := 0, 0
	for _, res := range r.results {
		if res.pass {
			pass++
		} else {
			fail++
		}
	}
	fmt.Fprintf(&b, "# Lab report\n\n")
	fmt.Fprintf(&b, "| Item | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Version | %s |\n", cell(r.version))
	fmt.Fprintf(&b, "| Started | %s |\n", r.started.Format(time.RFC3339))
	fmt.Fprintf(&b, "| Finished | %s |\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "| Kernel | %s |\n", cell(strings.TrimSpace(string(kernel))))
	fmt.Fprintf(&b, "| sing-box | %s |\n", cell(r.singBox))
	fmt.Fprintf(&b, "| Go | %s %s/%s |\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "| Result | %d passed, %d failed |\n\n", pass, fail)
	fmt.Fprintf(&b, "## Scenarios\n\n| Test | Scenario | Result | Note |\n|---|---|---|---|\n")
	for _, res := range r.results {
		verdict := "PASS"
		if !res.pass {
			verdict = "FAIL"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", cell(res.test), cell(res.scenario), verdict, cell(res.note))
	}
	b.WriteString("\nProcess logs are in the per-test folders next to this file.\n")
	return b.String()
}

// cell makes s safe inside a Markdown table cell.
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	return strings.Join(strings.Fields(s), " ")
}
