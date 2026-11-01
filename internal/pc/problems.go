package pc

import (
	"errors"
	"net/netip"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nikoejento/obhod/internal/pc/status"
)

// Problems: things that do not work although the app is connected. A failure of the desync engine never ends
// the connection; it is recorded here and shown to the user (Snapshot.Problems, announced as a state event).

const (
	msgDesyncFailed       = "The desync program could not start. Picky sites may not open."
	msgTunnelEngineFailed = "The tunnel program stopped working. Blocked sites may not open."

	maxProblemDetail = 2048 // bytes of Problem.Detail
	maxDetailLine    = 200  // runes of one output line
)

// Problems returns the current problems in the order they first appeared. It is never nil.
func (a *App) Problems() []status.Problem {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]status.Problem{}, a.problems...)
}

// setProblem records p, replacing the entry with the same code, and announces a state event when it changed.
func (a *App) setProblem(p status.Problem) {
	a.mu.Lock()
	changed := true
	replaced := false
	for i := range a.problems {
		if a.problems[i].Code == p.Code {
			changed = a.problems[i] != p
			a.problems[i], replaced = p, true
			break
		}
	}
	if !replaced {
		a.problems = append(a.problems, p)
	}
	a.mu.Unlock()
	a.log.Warn("problem", "code", p.Code)
	if changed {
		a.publish(Event{Kind: EventState})
	}
}

// clearProblem removes the entry with the given code (no-op when absent).
func (a *App) clearProblem(code string) {
	a.mu.Lock()
	out := a.problems[:0:0]
	for _, p := range a.problems {
		if p.Code != code {
			out = append(out, p)
		}
	}
	changed := len(out) != len(a.problems)
	a.problems = out
	a.mu.Unlock()
	if changed {
		a.publish(Event{Kind: EventState})
	}
}

// clearProblems removes every entry; the caller publishes the state event that follows.
func (a *App) clearProblems() {
	a.mu.Lock()
	a.problems = nil
	a.mu.Unlock()
}

// desyncProblem records that the desync engine failed. cause may be nil; name is the process whose output
// belongs to the failure ("" for a failure before the process started).
func (a *App) desyncProblem(name string, cause error) {
	a.setProblem(status.Problem{Code: status.ProblemDesyncFailed, Message: msgDesyncFailed, Detail: a.problemDetail(name, cause)})
}

// problemDetail builds the sanitized Detail: the cause and the last output lines of the process.
func (a *App) problemDetail(name string, cause error) string {
	var lines []string
	if name != "" {
		lines = a.d.procs.Logs(name)
	}
	var c string
	var ee *exitError
	switch {
	case cause == nil:
	case errors.As(cause, &ee) && len(ee.commands) > 0:
		c = "one-time setup is needed: the firewall rules for the desync program are missing or out of date"
	default:
		c = cause.Error()
	}
	return buildDetail(c, lines, a.currentSecret())
}

// isEngineName reports whether name is a desync engine process (not sing-box).
func isEngineName(name string) bool { return name != nameSingBox && name != "" }

// procFailed handles the procman event "failed" for a process: it records the problem for the desync engine.
// (sing-box is handled by singBoxFailed, which disconnects.) Events that arrive while not connected are stale.
func (a *App) procFailed(name string, cause error) {
	if !isEngineName(name) {
		return
	}
	if st := a.State(); st != StateConnected && st != StateConnecting {
		return
	}
	a.desyncProblem(name, cause)
}

// buildDetail makes the text of a Problem.Detail: "Exit: <cause>" and the output lines, each sanitized and
// capped; the oldest output lines are dropped first when the whole is above maxProblemDetail bytes.
func buildDetail(cause string, output []string, secrets ...string) string {
	head := ""
	if c := sanitizeLine(cause, secrets); c != "" {
		head = "Exit: " + c
	}
	var lines []string
	for _, l := range output {
		// One process line can hold several after a carriage return rewrite; sanitize each piece.
		for _, p := range strings.Split(l, "\n") {
			if p = sanitizeLine(p, secrets); p != "" {
				lines = append(lines, p)
			}
		}
	}
	if len(lines) > procLogTail {
		lines = lines[len(lines)-procLogTail:]
	}
	join := func(ls []string) string {
		var parts []string
		if head != "" {
			parts = append(parts, head)
		}
		if len(ls) > 0 {
			parts = append(parts, "Output:")
			parts = append(parts, ls...)
		}
		return strings.Join(parts, "\n")
	}
	out := join(lines)
	for len(out) > maxProblemDetail && len(lines) > 0 {
		lines = lines[1:]
		out = join(lines)
	}
	return truncateBytes(out, maxProblemDetail)
}

// truncateBytes cuts s to at most n bytes on a rune boundary.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// The patterns of sanitizeLine. Order matters: paths first (they contain dots and colons), addresses next,
// then host names.
var (
	winPathRe  = regexp.MustCompile(`(?:\b[A-Za-z]:|\\\\[^\\/\s"'<>|]+\\[^\\/\s"'<>|]+)[\\/](?:[^\\/:*?"<>|\r\n]+[\\/])*[^\\/:*?"<>|\s]*|\\\\\?\\[A-Za-z]:[\\/](?:[^\\/:*?"<>|\r\n]+[\\/])*[^\\/:*?"<>|\s]*`)
	unixPathRe = regexp.MustCompile(`(^|[^\w./\-])((?:/[\w.\-@+~]+){2,}/?)`)
	bracketRe  = regexp.MustCompile(`\[([0-9A-Fa-f:.]+)(?:%[^\]\s]*)?\](?::\d{1,5})?`)
	v6candRe   = regexp.MustCompile(`[0-9A-Fa-f:.]{3,}(?:%[A-Za-z0-9_.\-]+)?`)
	v4Re       = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b(?::\d{1,5})?`)
	hostRe     = regexp.MustCompile(`\b[A-Za-z0-9](?:[A-Za-z0-9\-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9\-]*[A-Za-z0-9])?)+\b(?::\d{1,5})?`)
	localRe    = regexp.MustCompile(`\blocalhost(?::\d{1,5})?\b`)
	uuidRe     = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	tokenRe    = regexp.MustCompile(`[A-Za-z0-9+/_\-]{24,}={0,2}`)
)

// fileExts are the last labels of a dotted word that stay (they are file names, not host names).
var fileExts = map[string]bool{
	"exe": true, "dll": true, "sys": true, "so": true, "json": true, "yaml": true, "yml": true, "txt": true,
	"log": true, "bin": true, "conf": true, "cfg": true, "lua": true, "dat": true, "db": true, "new": true,
	"old": true, "tmp": true, "bak": true, "go": true, "sh": true, "md": true, "ini": true, "toml": true,
	"nft": true, "pid": true, "lock": true, "cmd": true, "bat": true, "ps1": true, "msi": true,
}

// sanitizeLine makes one line safe to show: control characters and colour codes are removed, absolute paths
// shrink to the file name, IPv4/IPv6 addresses, host names and host:port pairs are replaced by <addr> or
// <host>, the given secrets, UUIDs and long token-like words by <secret>, and the line is capped to
// maxDetailLine runes. The result has no leading or trailing space.
func sanitizeLine(s string, secrets []string) string {
	s = strings.ToValidUTF8(s, "")
	s = ansiEscape.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case unicode.IsControl(r), r == ' ', r == ' ':
			return -1
		}
		return r
	}, s)

	for _, sec := range secrets {
		if len(sec) >= 4 {
			s = strings.ReplaceAll(s, sec, "<secret>")
		}
	}
	s = winPathRe.ReplaceAllStringFunc(s, baseOfPath)
	s = unixPathRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := unixPathRe.FindStringSubmatch(m)
		return sub[1] + baseOfPath(sub[2])
	})
	s = uuidRe.ReplaceAllString(s, "<secret>")

	s = bracketRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := bracketRe.FindStringSubmatch(m)
		if _, err := netip.ParseAddr(sub[1]); err == nil {
			return "<addr>"
		}
		return m
	})
	s = v6candRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.Count(m, ":") < 2 {
			return m
		}
		core := m
		if i := strings.IndexByte(core, '%'); i >= 0 {
			core = core[:i]
		}
		if _, err := netip.ParseAddr(core); err == nil && len(core) >= 3 {
			return "<addr>"
		}
		return m
	})
	s = v4Re.ReplaceAllString(s, "<addr>")
	s = localRe.ReplaceAllString(s, "<addr>")
	s = hostRe.ReplaceAllStringFunc(s, func(m string) string {
		w := m
		if i := strings.LastIndexByte(w, ':'); i >= 0 {
			w = w[:i]
		}
		last := w[strings.LastIndexByte(w, '.')+1:]
		if fileExts[strings.ToLower(last)] || !isLetters(last) || strings.Contains(m, "<") {
			return m
		}
		return "<host>"
	})
	s = tokenRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.ContainsAny(m, "0123456789") && strings.ContainsFunc(m, unicode.IsLetter) {
			return "<secret>"
		}
		return m
	})

	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxDetailLine {
		r := []rune(s)
		s = string(r[:maxDetailLine-1]) + "…"
	}
	return s
}

func isLetters(s string) bool {
	if len(s) < 2 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// baseOfPath returns the last element of a path (either separator style), or "<path>" for a bare root.
func baseOfPath(p string) string {
	p = strings.TrimRight(p, `\/`)
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		p = p[i+1:]
	}
	if p == "" || strings.HasSuffix(p, ":") {
		return "<path>"
	}
	return p
}
