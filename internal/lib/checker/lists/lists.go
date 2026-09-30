// Package lists loads and merges domain lists: candidates to check and static proxy/block entries.
package lists

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/nikoejento/obhod/internal/lib/route"
)

// maxLine bounds a single list line; longer lines are a read error rather than silent truncation.
const maxLine = 1 << 20

const bom = "\uFEFF" // UTF-8 byte order mark

// Config names the list files.
type Config struct {
	Candidates []string // files; domains the checker decides for
	Proxy      []string // files; static proxy route
	Block      []string // files; static block route
}

// Lists is the merged result of Load.
type Lists struct {
	Candidates []string      // normalized, deduped, sorted
	Static     []route.Entry // Source = static, sorted by domain
	Warnings   []string      // skipped lines, as "file:line: reason"
}

// Parse reads one list: one domain per line, `#` comments (whole-line and trailing), blank lines ignored,
// a leading "*." or "." stripped. Invalid lines become warnings "name:line: reason". The result is deduped
// and keeps first-seen order. err is only a read error.
func Parse(r io.Reader, name string) (domains []string, warnings []string, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	seen := map[string]bool{}
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if line == 1 {
			text = strings.TrimPrefix(text, bom)
		}
		if i := strings.IndexByte(text, '#'); i >= 0 {
			text = text[:i]
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		text = strings.TrimPrefix(strings.TrimPrefix(text, "*."), ".")
		d, nerr := route.Normalize(text)
		if nerr != nil {
			warnings = append(warnings, fmt.Sprintf("%s:%d: %v", name, line, nerr))
			continue
		}
		if !seen[d] {
			seen[d] = true
			domains = append(domains, d)
		}
	}
	return domains, warnings, sc.Err()
}

func loadGroup(files []string) (domains []string, warnings []string, err error) {
	for _, f := range files {
		fh, oerr := os.Open(f)
		if oerr != nil {
			return nil, nil, fmt.Errorf("lists: %w", oerr)
		}
		d, w, perr := Parse(fh, f)
		fh.Close()
		if perr != nil {
			return nil, nil, fmt.Errorf("lists: %s: %w", f, perr)
		}
		domains = append(domains, d...)
		warnings = append(warnings, w...)
	}
	return domains, warnings, nil
}

// Load reads every configured file and merges them. Precedence is block > proxy > candidate: a domain in a
// static list is removed from Candidates, and one in both static lists is blocked. A missing or unreadable
// file is an error.
func Load(cfg Config) (Lists, error) {
	var out Lists
	cand, w, err := loadGroup(cfg.Candidates)
	if err != nil {
		return Lists{}, err
	}
	out.Warnings = append(out.Warnings, w...)
	proxy, w, err := loadGroup(cfg.Proxy)
	if err != nil {
		return Lists{}, err
	}
	out.Warnings = append(out.Warnings, w...)
	block, w, err := loadGroup(cfg.Block)
	if err != nil {
		return Lists{}, err
	}
	out.Warnings = append(out.Warnings, w...)

	static := map[string]route.Route{}
	for _, d := range block {
		static[d] = route.Block
	}
	for _, d := range proxy {
		if _, ok := static[d]; !ok {
			static[d] = route.Proxy
		}
	}
	for d, r := range static {
		out.Static = append(out.Static, route.Entry{Domain: d, Route: r, Source: route.SourceStatic})
	}
	sort.Slice(out.Static, func(i, j int) bool { return out.Static[i].Domain < out.Static[j].Domain })

	seen := map[string]bool{}
	for _, d := range cand {
		if _, isStatic := static[d]; isStatic || seen[d] {
			continue
		}
		seen[d] = true
		out.Candidates = append(out.Candidates, d)
	}
	sort.Strings(out.Candidates)
	return out, nil
}
