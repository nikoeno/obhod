package lab

// This file has no build tag on purpose: parsing the commands splitter prints is pure Go, so its
// unit test runs on any OS without root.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The lines of splitter's exit-2 message (internal/pc checkNFT) that introduce the Setup commands
// and the Teardown commands that follow them.
const (
	nftSetupHeader    = "Apply these commands as root"
	nftTeardownHeader = "Remove the rules later with:"
)

// parseNFTSetup extracts the nft Setup commands from the message splitter prints before it exits
// with code 2 (the lines indented under "Apply these commands as root ..." up to the Teardown
// header) and returns each as an argv for exec (no shell). It accepts only what the lab is willing
// to run inside the client namespace: every line must either start with `nft`, use the verb add or
// flush and name the table "inet splitter", or be exactly the routing exemption
// `ip rule add fwmark 0x40000000/0x40000000 lookup main priority <n>` (see isEngineMarkRule).
// A line holding anything else is an error, so a changed message
// can never make the lab run an arbitrary command. Teardown commands are never returned.
func parseNFTSetup(out string) ([][]string, error) {
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, nftSetupHeader) {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, errors.New("lab: no Setup header in the splitter output")
	}
	var cmds [][]string
	for _, l := range lines[start:] {
		if strings.Contains(l, nftTeardownHeader) || strings.TrimSpace(l) == "" {
			break
		}
		if !strings.HasPrefix(l, "  ") {
			return nil, fmt.Errorf("lab: unexpected line in the Setup commands: %q", l)
		}
		argv, err := shellWords(strings.TrimSpace(l))
		if err != nil {
			return nil, fmt.Errorf("lab: Setup command %q: %w", l, err)
		}
		if isEngineMarkRule(argv) {
			cmds = append(cmds, argv)
			continue
		}
		if len(argv) < 2 || argv[0] != "nft" {
			return nil, fmt.Errorf("lab: Setup command %q is not an nft command", l)
		}
		verb, _, _ := strings.Cut(argv[1], " ")
		if verb != "add" && verb != "flush" {
			return nil, fmt.Errorf("lab: Setup command %q: verb %q is not add or flush", l, verb)
		}
		if !strings.Contains(strings.Join(argv[1:], " "), "inet splitter") {
			return nil, fmt.Errorf("lab: Setup command %q does not name the table inet splitter", l)
		}
		cmds = append(cmds, argv)
	}
	if len(cmds) == 0 {
		return nil, errors.New("lab: the splitter output has no Setup commands")
	}
	return cmds, nil
}

// engineMarkSpec is the nfqws2 mark with its mask, as the app prints it in `ip rule add fwmark`.
// It must stay equal to nfqws.DefaultMark (the lab cannot import it).
const engineMarkSpec = "0x40000000/0x40000000"

// isEngineMarkRule reports whether argv is exactly `ip rule add fwmark 0x40000000/0x40000000
// lookup main priority <n>` with a decimal <n>: the only `ip` command the lab will run.
func isEngineMarkRule(argv []string) bool {
	if len(argv) != 9 {
		return false
	}
	for i, w := range []string{"ip", "rule", "add", "fwmark", engineMarkSpec, "lookup", "main", "priority"} {
		if argv[i] != w {
			return false
		}
	}
	n, err := strconv.ParseUint(argv[8], 10, 16)
	return err == nil && strconv.FormatUint(n, 10) == argv[8]
}

// shellWords splits s like a POSIX shell would, for the two forms the app prints: bare words
// separated by blanks and single-quoted strings (which may hold spaces, braces, double quotes and
// semicolons). It rejects everything else a shell would interpret: double quotes, backslashes, `$`,
// backquotes, redirections, pipes, `;` and `&` outside single quotes.
func shellWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, errors.New("unterminated single quote")
			}
			cur.WriteString(s[i+1 : i+1+j])
			inWord = true
			i += j + 1
		case c == ' ' || c == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case strings.IndexByte("\"\\$`<>|;&(){}*?[]#~!\n", c) >= 0:
			return nil, fmt.Errorf("shell syntax %q outside single quotes", string(c))
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// logErrValue returns the unquoted err="..." value of the last slog text line that contains marker.
func logErrValue(log, marker string) (string, error) {
	lines := strings.Split(log, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if !strings.Contains(l, marker) {
			continue
		}
		_, rest, ok := strings.Cut(l, ` err=`)
		if !ok {
			return "", fmt.Errorf("no err= in the log line %q", l)
		}
		if strings.HasPrefix(rest, `"`) {
			// A quoted value runs to the closing unescaped quote.
			for j := 1; j < len(rest); j++ {
				if rest[j] == 0x5c {
					j++
				} else if rest[j] == '"' {
					rest = rest[:j+1]
					break
				}
			}
			return strconv.Unquote(rest)
		}
		return rest, nil
	}
	return "", fmt.Errorf("no log line with %q", marker)
}
