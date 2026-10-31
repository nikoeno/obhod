package servers

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// link is a share link taken apart without net/url, so a malformed link never
// produces an error text that quotes it.
type link struct {
	scheme   string
	user     string // userinfo, percent-decoded ("" when absent)
	hasUser  bool
	hostport string // as written, after the last "@"
	query    query
	fragment string
}

// query holds the decoded options of a link; the first value of a key wins.
type query map[string][]string

func (q query) get(keys ...string) string {
	for _, k := range keys {
		if v := q[k]; len(v) > 0 && v[0] != "" {
			return v[0]
		}
	}
	return ""
}

// flag reports whether any of the keys is set to 1, true or yes.
func (q query) flag(keys ...string) bool {
	switch strings.ToLower(q.get(keys...)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// parseQuery splits on "&" (not on ";", which ss plugin options use) and
// decodes leniently: a bad escape keeps the raw text.
func parseQuery(s string) query {
	q := query{}
	for _, part := range strings.Split(s, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		k = unescapeQuery(k)
		q[k] = append(q[k], unescapeQuery(v))
	}
	return q
}

func unescapeQuery(s string) string {
	if u, err := url.QueryUnescape(s); err == nil {
		return u
	}
	return s
}

func unescapePath(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

// splitLink takes scheme://[userinfo@]host[:port][/path][?query][#fragment] apart.
func splitLink(s string) (link, *Error) {
	i := strings.Index(s, "://")
	if i <= 0 {
		return link{}, invalid("This is not a server link.")
	}
	l := link{scheme: strings.ToLower(s[:i])}
	rest := s[i+3:]
	if j := strings.IndexByte(rest, '#'); j >= 0 {
		l.fragment = unescapePath(rest[j+1:])
		rest = rest[:j]
	}
	if j := strings.IndexByte(rest, '?'); j >= 0 {
		l.query = parseQuery(rest[j+1:])
		rest = rest[:j]
	} else {
		l.query = query{}
	}
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		l.user, l.hasUser = unescapePath(rest[:at]), true
		rest = rest[at+1:]
	}
	l.hostport = rest
	return l, nil
}

// splitHostPort separates the host and the port text; the port text may be
// empty. IPv6 literals must be in brackets.
func splitHostPort(hp string) (host, port string, err *Error) {
	if strings.HasPrefix(hp, "[") {
		end := strings.IndexByte(hp, ']')
		if end < 0 {
			return "", "", invalid("The link's server address is malformed.")
		}
		host, hp = hp[1:end], hp[end+1:]
		if hp == "" {
			return host, "", nil
		}
		if hp[0] != ':' {
			return "", "", invalid("The link's server address is malformed.")
		}
		return host, hp[1:], nil
	}
	if i := strings.LastIndexByte(hp, ':'); i >= 0 {
		host, port = hp[:i], hp[i+1:]
		if strings.Contains(host, ":") {
			return "", "", invalid("The link's server address is malformed (an IPv6 address needs brackets).")
		}
		return host, port, nil
	}
	return hp, "", nil
}

// hostPortText returns the validated host and the raw port text of a link.
func (l link) hostPortText() (string, string, *Error) {
	host, ptext, err := splitHostPort(l.hostport)
	if err != nil {
		return "", "", err
	}
	if host == "" {
		return "", "", invalid("The link has no server address.")
	}
	if !validHost(host) {
		return "", "", invalid("The link's server address is not valid.")
	}
	return host, ptext, nil
}

// hostPort returns the validated host and the single port of a link.
func (l link) hostPort() (string, int, *Error) {
	host, ptext, err := l.hostPortText()
	if err != nil {
		return "", 0, err
	}
	if ptext == "" {
		return "", 0, invalid("The link has no port.")
	}
	port, ok := parsePort(ptext)
	if !ok {
		return "", 0, invalid("The link's port is not a number from 1 to 65535.")
	}
	return host, port, nil
}

func parsePort(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	return n, true
}

// validHost accepts an IP literal (no zone) or a host name of letters, digits,
// "-", "_" and ".".
func validHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Zone() == ""
	}
	for _, r := range h {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return !strings.HasPrefix(h, ".") && !strings.Contains(h, "..")
}

// cleanName makes a display name from a link fragment: printable characters
// only, at most MaxNameRunes.
func cleanName(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if !unicode.IsPrint(r) {
			r = ' '
		}
		if n >= MaxNameRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// ValidName reports whether s is a usable name: 1 to MaxNameRunes printable
// characters, not only spaces.
func ValidName(s string) bool {
	n := utf8.RuneCountInString(s)
	if n < 1 || n > MaxNameRunes || strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

var fingerprints = map[string]bool{
	"chrome": true, "firefox": true, "edge": true, "safari": true, "360": true, "qq": true,
	"ios": true, "android": true, "random": true, "randomized": true,
}

// tlsFromQuery builds the sing-box "tls" object from the usual share-link
// options. defaultSecurity applies when the link has no "security". nil means
// no TLS.
func tlsFromQuery(q query, defaultSecurity string) (map[string]any, *Error) {
	sec := strings.ToLower(q.get("security"))
	if sec == "" {
		sec = defaultSecurity
	}
	switch sec {
	case "none", "":
		return nil, nil
	case "tls", "reality":
	default:
		return nil, unsupported("The link's security option is not supported.")
	}
	t := map[string]any{"enabled": true}
	if sni := q.get("sni", "peer", "serverName"); sni != "" {
		t["server_name"] = sni
	}
	if list := splitList(q.get("alpn")); len(list) > 0 {
		t["alpn"] = list
	}
	if q.flag("allowInsecure", "insecure", "allow_insecure") {
		t["insecure"] = true
	}
	fp := strings.ToLower(q.get("fp"))
	if fp != "" && fp != "none" && !fingerprints[fp] {
		return nil, invalid("The link's TLS fingerprint is not recognized.")
	}
	if sec == "reality" {
		pbk := q.get("pbk")
		if pbk == "" {
			return nil, invalid("The link uses Reality but has no public key.")
		}
		if t["server_name"] == nil {
			return nil, invalid("The link uses Reality but has no server name.")
		}
		if fp == "" || fp == "none" {
			fp = "chrome"
		}
		r := map[string]any{"enabled": true, "public_key": pbk}
		if sid := q.get("sid"); sid != "" {
			r["short_id"] = sid
		}
		t["reality"] = r
	}
	if fp != "" && fp != "none" {
		t["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
	}
	return t, nil
}

// transportFromQuery builds the sing-box "transport" object. nil means plain
// TCP. Transports only Xray offers (XHTTP, mKCP) are unsupported.
func transportFromQuery(q query) (map[string]any, *Error) {
	typ := strings.ToLower(q.get("type", "net"))
	hdr := strings.ToLower(q.get("headerType"))
	host := q.get("host")
	path := q.get("path")
	switch typ {
	case "", "tcp", "raw":
		if hdr != "" && hdr != "none" {
			return nil, unsupported("HTTP header obfuscation on a plain TCP server is not supported.")
		}
		return nil, nil
	case "ws", "websocket":
		t := map[string]any{"type": "ws"}
		if path != "" {
			if p, ed, ok := splitEarlyData(path); ok {
				path = p
				t["max_early_data"] = ed
				t["early_data_header_name"] = "Sec-WebSocket-Protocol"
			}
			t["path"] = path
		}
		if host != "" {
			t["headers"] = map[string]any{"Host": host}
		}
		return t, nil
	case "grpc", "gun":
		t := map[string]any{"type": "grpc"}
		if sn := q.get("serviceName", "service_name"); sn != "" {
			t["service_name"] = sn
		}
		return t, nil
	case "http", "h2":
		t := map[string]any{"type": "http"}
		if host != "" {
			t["host"] = splitList(host)
		}
		if path != "" {
			t["path"] = path
		}
		return t, nil
	case "httpupgrade":
		t := map[string]any{"type": "httpupgrade"}
		if host != "" {
			t["host"] = host
		}
		if path != "" {
			t["path"] = path
		}
		return t, nil
	}
	return nil, unsupported("This transport is not supported by the built-in engine.")
}

// splitEarlyData turns "/p?ed=2048" into ("/p", 2048).
func splitEarlyData(path string) (string, int, bool) {
	p, qs, ok := strings.Cut(path, "?")
	if !ok {
		return path, 0, false
	}
	n, err := strconv.Atoi(parseQuery(qs).get("ed"))
	if err != nil || n <= 0 {
		return path, 0, false
	}
	return p, n, true
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

// isUUID checks the canonical 8-4-4-4-12 hexadecimal form.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
				return false
			}
		}
	}
	return true
}
