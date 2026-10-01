// Command fetch downloads the pinned third-party binaries listed in
// third_party/manifest.yaml, verifies every archive and extracted file
// against its sha256, and installs them into third_party/bin/<os>-<arch>/.
//
// It is a developer tool, not part of any shipped binary. It only touches the
// output directory and its own download cache, and never runs what it fetches.
//
// Usage:
//
//	go run ./scripts/fetch                 # this machine's platform
//	go run ./scripts/fetch -platform all   # every platform in the manifest
//	go run ./scripts/fetch -check          # verify installed files, no network
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	manifest := fs.String("manifest", filepath.Join("third_party", "manifest.yaml"), "manifest file")
	out := fs.String("out", filepath.Join("third_party", "bin"), "output directory")
	platform := fs.String("platform", hostPlatform(), `platforms to fetch: comma list (e.g. "windows-amd64,linux-arm64") or "all"`)
	only := fs.String("only", "", "fetch only these binaries (comma list of manifest names)")
	cache := fs.String("cache", defaultCacheDir(), "download cache directory")
	check := fs.Bool("check", false, "only verify installed files against the manifest; no network")
	timeout := fs.Duration("timeout", 10*time.Minute, "overall time limit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "fetch: unexpected arguments: %v\n", fs.Args())
		return 2
	}

	m, err := loadManifest(*manifest)
	if err != nil {
		fmt.Fprintln(stderr, "fetch:", err)
		return 1
	}
	jobs, err := selectJobs(m, splitList(*platform), splitList(*only))
	if err != nil {
		fmt.Fprintln(stderr, "fetch:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	f := &fetcher{
		client:   &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}},
		cacheDir: *cache,
		outDir:   *out,
		log:      stdout,
	}
	failed := 0
	for _, j := range jobs {
		var err error
		if *check {
			err = f.verify(j)
		} else {
			err = f.install(ctx, j)
		}
		if err != nil {
			fmt.Fprintf(stderr, "fetch: %s %s: %v\n", j.Name, j.Platform, err)
			failed++
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// hostPlatform names this machine in manifest terms, e.g. "windows-amd64".
func hostPlatform() string { return runtime.GOOS + "-" + runtime.GOARCH }

func defaultCacheDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "obhod", "third_party")
	}
	return filepath.Join(os.TempDir(), "obhod-third_party")
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// job is one binary for one platform.
type job struct {
	Name     string
	Version  string
	Platform string
	Spec     Platform
}

func selectJobs(m Manifest, platforms, only []string) ([]job, error) {
	all := len(platforms) == 1 && platforms[0] == "all"
	want := map[string]bool{}
	for _, p := range platforms {
		want[p] = true
	}
	names := map[string]bool{}
	for _, n := range only {
		names[n] = true
	}

	var jobs []job
	foundName := map[string]bool{}
	foundPlatform := map[string]bool{}
	for _, b := range m.Binaries {
		if len(names) > 0 && !names[b.Name] {
			continue
		}
		foundName[b.Name] = true
		for _, p := range sortedKeys(b.Platforms) {
			if !all && !want[p] {
				continue
			}
			foundPlatform[p] = true
			jobs = append(jobs, job{Name: b.Name, Version: b.Version, Platform: p, Spec: b.Platforms[p]})
		}
	}
	for n := range names {
		if !foundName[n] {
			return nil, fmt.Errorf("binary %q is not in the manifest", n)
		}
	}
	if !all {
		for p := range want {
			if !foundPlatform[p] {
				return nil, fmt.Errorf("no binaries for platform %q in the manifest", p)
			}
		}
	}
	if len(jobs) == 0 {
		return nil, errors.New("nothing selected")
	}
	return jobs, nil
}
