package lab

// This file has no build tag on purpose: the log and file parsers of the lifecycle scenarios are
// pure Go, so their unit test runs on any OS without root.

import (
	"strings"
	"time"
)

// logLineTimes returns the time= attribute of every slog text line of log that contains sub, in file
// order. A line without a parsable time= attribute is skipped.
func logLineTimes(log, sub string) []time.Time {
	var out []time.Time
	for _, l := range strings.Split(log, "\n") {
		if !strings.Contains(l, sub) {
			continue
		}
		_, rest, ok := strings.Cut(l, "time=")
		if !ok {
			continue
		}
		v, _, _ := strings.Cut(rest, " ")
		t, err := time.Parse(time.RFC3339Nano, strings.Trim(v, `"`))
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	return out
}

// hostlistHas reports whether the hostlist file content (one name per line) names domain exactly.
func hostlistHas(hostlist []byte, domain string) bool {
	for _, f := range strings.Fields(string(hostlist)) {
		if f == domain {
			return true
		}
	}
	return false
}

// curlExit extracts the exit code from an error text like "exit status 35: ..." (-1 when absent).
func curlExit(err error) int {
	if err == nil {
		return -1
	}
	_, rest, ok := strings.Cut(err.Error(), "exit status ")
	if !ok {
		return -1
	}
	n := 0
	i := 0
	for ; i < len(rest) && rest[i] >= '0' && rest[i] <= '9'; i++ {
		n = n*10 + int(rest[i]-'0')
	}
	if i == 0 {
		return -1
	}
	return n
}

// isCurlTimeout reports a curl timeout (exit 28 or "timed out").
func isCurlTimeout(err error) bool {
	return err != nil && (curlExit(err) == 28 || strings.Contains(strings.ToLower(err.Error()), "timed out"))
}

// isCurlReset reports a connection reset as curl shows it: exit 35, 52, 55 or 56, or the text
// "connection reset" / "unexpected eof".
func isCurlReset(err error) bool {
	if err == nil || isCurlTimeout(err) {
		return false
	}
	switch curlExit(err) {
	case 35, 52, 55, 56:
		return true
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "connection reset") || strings.Contains(m, "unexpected eof")
}
