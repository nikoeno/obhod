package servers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/netip"
	"strconv"
	"strings"
)

// Limits (docs: api/desktop decisions 22 and 23).
const (
	MaxServers    = 20
	MaxInputBytes = 32 << 10
	MaxNameRunes  = 60

	// EngineSingBox is the only engine in v1; "xray" is reserved for transports sing-box lacks.
	EngineSingBox = "singbox"

	maxJSONDepth = 6
	maxJSONNodes = 500
)

// The sing-box outbound types the app can manage.
var supportedTypes = map[string]bool{
	"vless": true, "vmess": true, "trojan": true, "shadowsocks": true, "hysteria2": true, "tuic": true,
}

// forbiddenKeys could take the tunnel's own traffic around the app's routing
// (decision 23). They are rejected at any depth of a raw outbound.
var forbiddenKeys = map[string]bool{
	"detour": true, "bind_interface": true, "inet4_bind_address": true, "inet6_bind_address": true,
	"routing_mark": true, "netns": true, "network_strategy": true, "network_type": true,
	"fallback_network_type": true,
}

// Parsed is a validated input: the sing-box outbound and a non-secret summary.
type Parsed struct {
	Outbound json.RawMessage // what singbox.Outbounds.Tunnel takes; no "tag"
	Protocol string          // the outbound "type"
	Host     string
	Port     int
	Engine   string
	Name     string // suggested by the link's fragment; "" when it has none
}

// Parse turns a share link (vless, trojan, ss, hy2/hysteria2, vmess, tuic) or
// one raw sing-box outbound JSON object into a Parsed. It is pure: no file, no
// network, no process. Its errors are *Error values with fixed sentences that
// never contain a value from the input.
func Parse(input string) (Parsed, error) {
	if len(input) > MaxInputBytes {
		return Parsed{}, newError(ErrTooLarge, "The link or JSON is too large.")
	}
	s := strings.TrimSpace(strings.TrimPrefix(input, bomMark))
	if s == "" {
		return Parsed{}, invalid("The input is empty.")
	}
	if s[0] == '{' {
		p, err := parseJSON(s)
		if err != nil {
			return Parsed{}, err
		}
		return p, nil
	}
	if strings.ContainsAny(s, "\r\n") {
		return Parsed{}, invalid("Paste one link at a time.")
	}
	i := strings.Index(s, "://")
	if i <= 0 {
		return Parsed{}, invalid("This is not a server link or a JSON object.")
	}
	var p Parsed
	var err *Error
	switch scheme := strings.ToLower(s[:i]); scheme {
	case "vless":
		p, err = parseVLESS(s)
	case "trojan":
		p, err = parseTrojan(s)
	case "ss":
		p, err = parseShadowsocks(s)
	case "hy2", "hysteria2":
		p, err = parseHysteria2(s)
	case "vmess":
		p, err = parseVMess(s)
	case "tuic":
		p, err = parseTUIC(s)
	default:
		return Parsed{}, unsupported("This kind of link is not supported.")
	}
	if err != nil {
		return Parsed{}, err
	}
	return p, nil
}

// finish validates an outbound object whatever its source, drops the tag and
// encodes it deterministically (sorted keys).
func finish(obj map[string]any, name string) (Parsed, *Error) {
	typ, _ := obj["type"].(string)
	if !supportedTypes[typ] {
		return Parsed{}, unsupported("This protocol is not supported.")
	}
	host, _ := obj["server"].(string)
	if host == "" {
		return Parsed{}, invalid("The server has no address.")
	}
	if !validHost(host) {
		return Parsed{}, invalid("The server address is not valid.")
	}
	port, ok := portOf(obj)
	if !ok {
		return Parsed{}, invalid("The server has no valid port (1 to 65535).")
	}
	if msg := credentialProblem(typ, obj); msg != "" {
		return Parsed{}, invalid(msg)
	}
	if tls, ok := obj["tls"].(map[string]any); ok {
		if r, ok := tls["reality"].(map[string]any); ok && truthy(r["enabled"]) {
			if s, _ := r["public_key"].(string); s == "" {
				return Parsed{}, invalid("Reality needs a public key.")
			}
			if s, _ := tls["server_name"].(string); s == "" {
				return Parsed{}, invalid("Reality needs a server name.")
			}
		}
	} else if obj["tls"] != nil {
		return Parsed{}, invalid("The TLS settings are not an object.")
	}
	if t, present := obj["transport"]; present {
		tm, ok := t.(map[string]any)
		if !ok {
			return Parsed{}, invalid("The transport settings are not an object.")
		}
		switch tt, _ := tm["type"].(string); tt {
		case "ws", "grpc", "http", "httpupgrade":
		default:
			return Parsed{}, unsupported("This transport is not supported by the built-in engine.")
		}
	}
	delete(obj, "tag")
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return Parsed{}, invalid("The server settings cannot be encoded.")
	}
	return Parsed{
		Outbound: json.RawMessage(bytes.TrimSpace(buf.Bytes())),
		Protocol: typ, Host: host, Port: port, Engine: EngineSingBox, Name: name,
	}, nil
}

func truthy(v any) bool { b, _ := v.(bool); return b }

// credentialProblem names what is missing for the protocol, "" when complete.
func credentialProblem(typ string, obj map[string]any) string {
	str := func(k string) string { s, _ := obj[k].(string); return s }
	switch typ {
	case "vless", "vmess":
		if str("uuid") == "" {
			return "The server has no user ID."
		}
	case "trojan", "hysteria2":
		if str("password") == "" {
			return "The server has no password."
		}
	case "shadowsocks":
		if str("method") == "" || str("password") == "" {
			return "The server has no cipher or password."
		}
	case "tuic":
		if str("uuid") == "" || str("password") == "" {
			return "The server has no user ID or password."
		}
	}
	return ""
}

// credentialOf returns the identity part that tells two servers on one
// address apart. It is compared in memory and never stored or shown.
func credentialOf(typ string, obj map[string]any) string {
	str := func(k string) string { s, _ := obj[k].(string); return s }
	switch typ {
	case "vless", "vmess":
		return strings.ToLower(str("uuid"))
	case "trojan", "hysteria2":
		return str("password")
	case "shadowsocks":
		return str("method") + ":" + str("password")
	case "tuic":
		return strings.ToLower(str("uuid")) + ":" + str("password")
	}
	return ""
}

// portOf returns server_port, or the first port of server_ports.
func portOf(obj map[string]any) (int, bool) {
	if v, present := obj["server_port"]; present {
		n, ok := toInt(v)
		return n, ok && n >= 1 && n <= 65535
	}
	if list, ok := obj["server_ports"].([]any); ok && len(list) > 0 {
		first, _ := list[0].(string)
		start, _, _ := strings.Cut(first, ":")
		n, err := strconv.Atoi(start)
		return n, err == nil && n >= 1 && n <= 65535
	}
	if list, ok := obj["server_ports"].([]string); ok && len(list) > 0 {
		start, _, _ := strings.Cut(list[0], ":")
		n, err := strconv.Atoi(start)
		return n, err == nil && n >= 1 && n <= 65535
	}
	return 0, false
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case json.Number:
		n, err := strconv.ParseInt(x.String(), 10, 32)
		return int(n), err == nil
	case float64:
		n := int(x)
		return n, float64(n) == x
	}
	return 0, false
}

// parseJSON parses one raw sing-box outbound object.
func parseJSON(input string) (Parsed, *Error) {
	dec := json.NewDecoder(strings.NewReader(input))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return Parsed{}, invalid("The JSON is not valid.")
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return Parsed{}, invalid("Paste exactly one JSON object.")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return Parsed{}, invalid("The JSON must be one object.")
	}
	nodes := 0
	if e := checkShape(obj, 1, &nodes); e != nil {
		return Parsed{}, e
	}
	if typ, _ := obj["type"].(string); typ == "" {
		return Parsed{}, invalid("The JSON has no \"type\".")
	}
	return finish(obj, "")
}

// checkShape enforces depth and size limits and rejects the forbidden keys.
func checkShape(v any, depth int, nodes *int) *Error {
	if depth > maxJSONDepth {
		return invalid("The JSON is nested too deeply.")
	}
	if *nodes++; *nodes > maxJSONNodes {
		return invalid("The JSON has too many fields.")
	}
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			if forbiddenKeys[k] {
				return invalid("The JSON sets how traffic leaves this computer (detour or bind options), which is not allowed.")
			}
			if e := checkShape(c, depth+1, nodes); e != nil {
				return e
			}
		}
	case []any:
		for _, c := range x {
			if e := checkShape(c, depth+1, nodes); e != nil {
				return e
			}
		}
	}
	return nil
}

// identity is the duplicate key of an outbound: protocol, host, port and credential.
func identity(outbound json.RawMessage) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(outbound))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return "", false
	}
	typ, _ := obj["type"].(string)
	host, _ := obj["server"].(string)
	port, _ := portOf(obj)
	if a, err := netip.ParseAddr(host); err == nil {
		host = a.String()
	}
	return typ + "|" + strings.ToLower(host) + "|" + strconv.Itoa(port) + "|" + credentialOf(typ, obj), true
}

// bomMark is a UTF-8 byte order mark that editors may put in front of pasted text.
const bomMark = "\xef\xbb\xbf"
