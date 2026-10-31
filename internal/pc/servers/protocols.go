package servers

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
)

// The share-link parsers. Each builds a sing-box outbound object and hands it
// to finish, which validates and encodes it. Error texts name the kind of
// problem, never a value.

func parseVLESS(s string) (Parsed, *Error) {
	l, e := splitLink(s)
	if e != nil {
		return Parsed{}, e
	}
	host, port, e := l.hostPort()
	if e != nil {
		return Parsed{}, e
	}
	if l.user == "" {
		return Parsed{}, invalid("The link has no user ID.")
	}
	if !isUUID(l.user) {
		return Parsed{}, invalid("The link's user ID is not a UUID.")
	}
	q := l.query
	if enc := strings.ToLower(q.get("encryption")); enc != "" && enc != "none" {
		return Parsed{}, unsupported("VLESS encryption is not supported.")
	}
	obj := map[string]any{"type": "vless", "server": host, "server_port": port, "uuid": l.user}
	switch flow := q.get("flow"); flow {
	case "":
	case "xtls-rprx-vision":
		obj["flow"] = flow
	default:
		return Parsed{}, unsupported("The link's flow option is not supported.")
	}
	if pe := q.get("packetEncoding"); pe == "xudp" || pe == "packetaddr" {
		obj["packet_encoding"] = pe
	}
	if e := addTLSAndTransport(obj, q, "none"); e != nil {
		return Parsed{}, e
	}
	return finish(obj, cleanName(l.fragment))
}

func addTLSAndTransport(obj map[string]any, q query, defaultSecurity string) *Error {
	tls, e := tlsFromQuery(q, defaultSecurity)
	if e != nil {
		return e
	}
	if tls != nil {
		obj["tls"] = tls
	}
	tr, e := transportFromQuery(q)
	if e != nil {
		return e
	}
	if tr != nil {
		obj["transport"] = tr
	}
	return nil
}

func parseTrojan(s string) (Parsed, *Error) {
	l, e := splitLink(s)
	if e != nil {
		return Parsed{}, e
	}
	host, port, e := l.hostPort()
	if e != nil {
		return Parsed{}, e
	}
	if l.user == "" {
		return Parsed{}, invalid("The link has no password.")
	}
	obj := map[string]any{"type": "trojan", "server": host, "server_port": port, "password": l.user}
	if e := addTLSAndTransport(obj, l.query, "tls"); e != nil {
		return Parsed{}, e
	}
	return finish(obj, cleanName(l.fragment))
}

// ssMethods are the ciphers of the pinned sing-box 1.14 shadowsocks outbound.
var ssMethods = map[string]bool{
	"none": true, "aes-128-gcm": true, "aes-192-gcm": true, "aes-256-gcm": true,
	"chacha20-ietf-poly1305": true, "xchacha20-ietf-poly1305": true,
	"2022-blake3-aes-128-gcm": true, "2022-blake3-aes-256-gcm": true, "2022-blake3-chacha20-poly1305": true,
	"aes-128-ctr": true, "aes-192-ctr": true, "aes-256-ctr": true,
	"aes-128-cfb": true, "aes-192-cfb": true, "aes-256-cfb": true,
	"rc4-md5": true, "chacha20-ietf": true, "xchacha20": true,
}

// decodeB64 accepts standard and URL alphabets, with or without padding.
func decodeB64(s string) (string, bool) {
	s = strings.TrimRight(s, "=")
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), true
		}
	}
	return "", false
}

func parseShadowsocks(s string) (Parsed, *Error) {
	rest := s[strings.Index(s, "://")+3:]
	name := ""
	if j := strings.IndexByte(rest, '#'); j >= 0 {
		name = cleanName(unescapePath(rest[j+1:]))
		rest = rest[:j]
	}
	var q query
	if j := strings.IndexByte(rest, '?'); j >= 0 {
		q = parseQuery(rest[j+1:])
		rest = rest[:j]
	} else {
		q = query{}
	}
	rest = strings.TrimRight(rest, "/")

	var method, password, hostport string
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		// SIP002: userinfo is base64(method:password), or percent-encoded method:password.
		user := unescapePath(rest[:at])
		hostport = rest[at+1:]
		if !strings.Contains(user, ":") {
			dec, ok := decodeB64(user)
			if !ok {
				return Parsed{}, invalid("The link's cipher and password are not valid base64.")
			}
			user = dec
		}
		method, password, _ = strings.Cut(user, ":")
	} else {
		// Legacy: base64(method:password@host:port).
		dec, ok := decodeB64(rest)
		if !ok {
			return Parsed{}, invalid("The link is not valid base64.")
		}
		at := strings.LastIndexByte(dec, '@')
		if at < 0 {
			return Parsed{}, invalid("The link has no server address.")
		}
		method, password, _ = strings.Cut(dec[:at], ":")
		hostport = dec[at+1:]
	}
	l := link{hostport: hostport}
	host, port, e := l.hostPort()
	if e != nil {
		return Parsed{}, e
	}
	if method == "" || password == "" {
		return Parsed{}, invalid("The link has no cipher or no password.")
	}
	if !ssMethods[method] {
		return Parsed{}, unsupported("The link's cipher is not supported.")
	}
	obj := map[string]any{"type": "shadowsocks", "server": host, "server_port": port, "method": method, "password": password}
	if plugin := q.get("plugin"); plugin != "" {
		pname, opts, _ := strings.Cut(plugin, ";")
		if pname != "obfs-local" && pname != "v2ray-plugin" {
			return Parsed{}, unsupported("The link's plugin is not supported.")
		}
		obj["plugin"] = pname
		if opts != "" {
			obj["plugin_opts"] = opts
		}
	}
	return finish(obj, name)
}

func parseHysteria2(s string) (Parsed, *Error) {
	l, e := splitLink(s)
	if e != nil {
		return Parsed{}, e
	}
	if l.user == "" {
		return Parsed{}, invalid("The link has no password.")
	}
	host, ptext, e := l.hostPortText()
	if e != nil {
		return Parsed{}, e
	}
	q := l.query
	var single int
	var ranges []string
	addRange := func(tok string) *Error {
		a, b, _ := strings.Cut(tok, "-")
		lo, ok1 := parsePort(a)
		hi, ok2 := parsePort(b)
		if !ok1 || !ok2 || lo > hi {
			return invalid("The link's port range is not valid.")
		}
		ranges = append(ranges, strconv.Itoa(lo)+":"+strconv.Itoa(hi))
		return nil
	}
	for _, tok := range append(splitList(ptext), splitList(q.get("mport"))...) {
		if strings.Contains(tok, "-") {
			if e := addRange(tok); e != nil {
				return Parsed{}, e
			}
			continue
		}
		n, ok := parsePort(tok)
		if !ok {
			return Parsed{}, invalid("The link's port is not a number from 1 to 65535.")
		}
		if single == 0 {
			single = n
		}
	}
	obj := map[string]any{"type": "hysteria2", "server": host, "password": l.user}
	switch {
	case len(ranges) > 0:
		obj["server_ports"] = ranges
	case single != 0:
		obj["server_port"] = single
	default:
		obj["server_port"] = 443
	}
	if len(ranges) > 0 && single != 0 {
		// A single port next to ranges: keep it as a one-port range.
		obj["server_ports"] = append(ranges, strconv.Itoa(single)+":"+strconv.Itoa(single))
	}
	if q.get("pinSHA256") != "" {
		return Parsed{}, invalid("Certificate pinning (pinSHA256) is not supported.")
	}
	if ob := strings.ToLower(q.get("obfs")); ob != "" && ob != "none" {
		if ob != "salamander" {
			return Parsed{}, unsupported("The link's obfuscation type is not supported.")
		}
		pw := q.get("obfs-password")
		if pw == "" {
			return Parsed{}, invalid("The link uses obfuscation but has no obfuscation password.")
		}
		obj["obfs"] = map[string]any{"type": "salamander", "password": pw}
	}
	obj["tls"] = quicTLS(q)
	return finish(obj, cleanName(l.fragment))
}

// quicTLS is the always-on TLS object of the QUIC-based protocols.
func quicTLS(q query) map[string]any {
	t := map[string]any{"enabled": true}
	if sni := q.get("sni", "peer"); sni != "" {
		t["server_name"] = sni
	}
	if list := splitList(q.get("alpn")); len(list) > 0 {
		t["alpn"] = list
	}
	if q.flag("insecure", "allowInsecure", "allow_insecure") {
		t["insecure"] = true
	}
	return t
}

func parseTUIC(s string) (Parsed, *Error) {
	l, e := splitLink(s)
	if e != nil {
		return Parsed{}, e
	}
	host, port, e := l.hostPort()
	if e != nil {
		return Parsed{}, e
	}
	uuid, password, _ := strings.Cut(l.user, ":")
	if uuid == "" {
		return Parsed{}, invalid("The link has no user ID.")
	}
	if !isUUID(uuid) {
		return Parsed{}, invalid("The link's user ID is not a UUID.")
	}
	if password == "" {
		return Parsed{}, invalid("The link has no password.")
	}
	q := l.query
	obj := map[string]any{"type": "tuic", "server": host, "server_port": port, "uuid": uuid, "password": password}
	if cc := strings.ToLower(q.get("congestion_control")); cc != "" {
		switch cc {
		case "cubic", "new_reno", "bbr":
			obj["congestion_control"] = cc
		default:
			return Parsed{}, unsupported("The link's congestion control is not supported.")
		}
	}
	if m := strings.ToLower(q.get("udp_relay_mode")); m != "" {
		if m != "native" && m != "quic" {
			return Parsed{}, unsupported("The link's UDP relay mode is not supported.")
		}
		obj["udp_relay_mode"] = m
	}
	if q.flag("reduce_rtt", "zero_rtt") {
		obj["zero_rtt_handshake"] = true
	}
	obj["tls"] = quicTLS(q)
	return finish(obj, cleanName(l.fragment))
}

// vmessLink is the usual base64-JSON share format (v2rayN).
type vmessLink struct {
	PS   string          `json:"ps"`
	Add  string          `json:"add"`
	Port json.RawMessage `json:"port"`
	ID   string          `json:"id"`
	Aid  json.RawMessage `json:"aid"`
	Scy  string          `json:"scy"`
	Net  string          `json:"net"`
	Type string          `json:"type"`
	Host string          `json:"host"`
	Path string          `json:"path"`
	TLS  string          `json:"tls"`
	SNI  string          `json:"sni"`
	ALPN string          `json:"alpn"`
	FP   string          `json:"fp"`
}

// rawInt reads a JSON number or numeric string; ok is false for anything else.
func rawInt(r json.RawMessage) (int, bool) {
	t := strings.Trim(strings.TrimSpace(string(r)), `"`)
	if t == "" || t == "null" {
		return 0, false
	}
	n, err := strconv.Atoi(t)
	return n, err == nil
}

var vmessSecurities = map[string]bool{"auto": true, "none": true, "zero": true, "aes-128-gcm": true, "chacha20-poly1305": true}

func parseVMess(s string) (Parsed, *Error) {
	rest := s[strings.Index(s, "://")+3:]
	if strings.Contains(rest, "@") {
		return parseVMessURL(s)
	}
	if j := strings.IndexByte(rest, '#'); j >= 0 { // some apps add a fragment name
		rest = rest[:j]
	}
	dec, ok := decodeB64(strings.TrimRight(rest, "/"))
	if !ok {
		return Parsed{}, invalid("The link is not valid base64.")
	}
	var v vmessLink
	if err := json.Unmarshal([]byte(dec), &v); err != nil {
		return Parsed{}, invalid("The link does not contain a valid VMess description.")
	}
	if v.Add == "" {
		return Parsed{}, invalid("The link has no server address.")
	}
	if !validHost(v.Add) {
		return Parsed{}, invalid("The link's server address is not valid.")
	}
	if len(v.Port) == 0 {
		return Parsed{}, invalid("The link has no port.")
	}
	port, ok := rawInt(v.Port)
	if !ok || port < 1 || port > 65535 {
		return Parsed{}, invalid("The link's port is not a number from 1 to 65535.")
	}
	if v.ID == "" {
		return Parsed{}, invalid("The link has no user ID.")
	}
	if !isUUID(v.ID) {
		return Parsed{}, invalid("The link's user ID is not a UUID.")
	}
	scy := strings.ToLower(v.Scy)
	if scy == "" {
		scy = "auto"
	}
	if !vmessSecurities[scy] {
		return Parsed{}, unsupported("The link's VMess cipher is not supported.")
	}
	aid := 0
	if len(v.Aid) > 0 {
		if n, ok := rawInt(v.Aid); ok && n >= 0 {
			aid = n
		} else if strings.Trim(string(v.Aid), `" `) != "" && string(v.Aid) != "null" {
			return Parsed{}, invalid("The link's alterId is not a number.")
		}
	}
	obj := map[string]any{"type": "vmess", "server": v.Add, "server_port": port, "uuid": v.ID, "security": scy, "alter_id": aid}
	// Map the JSON fields to the option names the shared builders read.
	q := query{}
	set := func(k, val string) {
		if val != "" {
			q[k] = []string{val}
		}
	}
	set("type", v.Net)
	set("headerType", v.Type)
	set("host", v.Host)
	set("path", v.Path)
	if strings.EqualFold(v.Net, "grpc") {
		set("serviceName", v.Path)
	}
	set("sni", v.SNI)
	set("alpn", v.ALPN)
	set("fp", v.FP)
	if strings.EqualFold(v.TLS, "tls") {
		set("security", "tls")
	} else {
		set("security", "none")
	}
	if e := addTLSAndTransport(obj, q, "none"); e != nil {
		return Parsed{}, e
	}
	return finish(obj, cleanName(v.PS))
}

// parseVMessURL handles the URL form vmess://uuid@host:port?options#name that
// some apps produce.
func parseVMessURL(s string) (Parsed, *Error) {
	l, e := splitLink(s)
	if e != nil {
		return Parsed{}, e
	}
	host, port, e := l.hostPort()
	if e != nil {
		return Parsed{}, e
	}
	if l.user == "" {
		return Parsed{}, invalid("The link has no user ID.")
	}
	if !isUUID(l.user) {
		return Parsed{}, invalid("The link's user ID is not a UUID.")
	}
	scy := strings.ToLower(l.query.get("encryption", "scy"))
	if scy == "" {
		scy = "auto"
	}
	if !vmessSecurities[scy] {
		return Parsed{}, unsupported("The link's VMess cipher is not supported.")
	}
	obj := map[string]any{"type": "vmess", "server": host, "server_port": port, "uuid": l.user, "security": scy, "alter_id": 0}
	if e := addTLSAndTransport(obj, l.query, "none"); e != nil {
		return Parsed{}, e
	}
	return finish(obj, cleanName(l.fragment))
}
