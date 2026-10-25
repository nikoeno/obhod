package lab

// This file has no build tag on purpose: the splitter config, list and decision-store files that
// RunSplitter writes are rendered by pure Go, so their unit test runs on any OS without root.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nikoejento/obhod/internal/lib/route"
)

// splitterDefaults are the lab's app.yaml defaults for one RunSplitter. Keys are the ones
// internal/pc reads strictly: snake_case from the children's yaml tags, and the checker's
// sections in lowercase Go field names (internal/pc/config.go).
type splitterDefaults struct {
	Root         string   // paths.root: a fresh temp dir
	DoH          string   // lab DoH URL (IP host), also singbox.dns.direct_doh
	Echo         string   // public-IP echo URL for checker.netwatch.echoendpoints
	Controls     []string // analyzer.control_domains
	Canaries     []string // analyzer.canary_domains
	Neutral      []string // analyzer.neutral_sni
	SingBox      string   // singbox.binary (the pinned sing-box)
	Desync       string   // desync.binary; empty: not set
	StatusListen string   // status.listen, loopback in the client namespace
	Candidates   string   // checker.lists.candidates file
	Proxy        string   // checker.lists.proxy file
	Block        string   // checker.lists.block file
}

// The checker schedule of the lab defaults: short, so a scenario sees several checks in a few
// minutes. The scheduler clamps Interval-Jitter to at least MinGap (schedule/API.md).
const (
	labCheckInterval = 15 * time.Second
	labCheckJitter   = 2 * time.Second
	labCheckMinGap   = 10 * time.Second
	labNetDebounce   = time.Second
	labNetRefresh    = 5 * time.Second
	labConfirmations = 2
)

// yaml renders the defaults as an app.yaml document.
func (d splitterDefaults) yaml() ([]byte, error) {
	strs := func(s []string) []any {
		out := make([]any, len(s))
		for i, v := range s {
			out[i] = v
		}
		return out
	}
	doc := map[string]any{
		"paths":   map[string]any{"root": d.Root},
		"secrets": map[string]any{"tunnel": "tunnel.json", "proxy": "proxy.json"},
		"analyzer": map[string]any{
			"concurrency":        2,
			"per_ip_concurrency": 1,
			"jitter":             []any{"0s", "50ms"},
			"doh":                []any{d.DoH},
			"control_domains":    strs(d.Controls),
			"canary_domains":     strs(d.Canaries),
			"neutral_sni":        strs(d.Neutral),
			// The lab's VPS sing-box is visible in the shared /proc; it is not the client's tool.
			"bypass_processes": []any{},
			"options": map[string]any{
				"stages": "tcp,tls,http",
				// uTLS's Chrome hello spans two segments and NaiveSNI ignores split hellos.
				"fingerprint": "firefox",
				"repeats":     2,
				"sni_swap":    true,
				"timeouts":    map[string]any{"resolve": "3s", "tcp": "2s", "tls": "3s", "http": "3s", "bulk": "4s"},
			},
		},
		"checker": map[string]any{
			"lists": map[string]any{
				"candidates": []any{d.Candidates},
				"proxy":      []any{d.Proxy},
				"block":      []any{d.Block},
			},
			"schedule": map[string]any{
				"interval": labCheckInterval.String(),
				"jitter":   labCheckJitter.String(),
				"mingap":   labCheckMinGap.String(),
			},
			"decide":         map[string]any{"confirmations": labConfirmations},
			"netwatch":       map[string]any{"debounce": labNetDebounce.String(), "refresh": labNetRefresh.String(), "echoendpoints": []any{d.Echo}},
			"unknowndefault": string(route.Tunnel),
		},
		"singbox": map[string]any{
			"binary": d.SingBox,
			"dns":    map[string]any{"direct_doh": d.DoH},
		},
		"status": map[string]any{"listen": d.StatusListen},
		"log":    map[string]any{"level": "debug"},
	}
	if d.Desync != "" {
		doc["desync"] = map[string]any{"binary": d.Desync}
	}
	return yaml.Marshal(doc)
}

// mergeYAML deep-merges the YAML mapping over onto base: mappings merge key by key, any other
// value of over (scalar, list, null) replaces base's. An empty over returns base re-encoded.
func mergeYAML(base, over []byte) ([]byte, error) {
	b, err := decodeMapping(base)
	if err != nil {
		return nil, fmt.Errorf("lab: base yaml: %w", err)
	}
	o, err := decodeMapping(over)
	if err != nil {
		return nil, fmt.Errorf("lab: override yaml: %w", err)
	}
	return yaml.Marshal(mergeMaps(b, o))
}

func decodeMapping(b []byte) (map[string]any, error) {
	m := map[string]any{}
	if len(bytes.TrimSpace(b)) == 0 {
		return m, nil
	}
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func mergeMaps(base, over map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		bm, bok := out[k].(map[string]any)
		om, ook := v.(map[string]any)
		if bok && ook {
			out[k] = mergeMaps(bm, om)
			continue
		}
		out[k] = v
	}
	return out
}

// yamlPath returns the value at a dotted key path of a YAML mapping (ok=false when absent).
func yamlPath(doc []byte, path string) (any, bool) {
	m, err := decodeMapping(doc)
	if err != nil {
		return nil, false
	}
	var cur any = m
	for _, k := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = mm[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// yamlString returns the string at path, or def when it is absent or not a string.
func yamlString(doc []byte, path, def string) string {
	if v, ok := yamlPath(doc, path); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

// listFile renders a checker list file (lists/API.md): one normalized domain per line.
func listFile(domains []string) ([]byte, error) {
	var out []string
	for _, d := range domains {
		n, err := route.Normalize(d)
		if err != nil {
			return nil, fmt.Errorf("lab: %w", err)
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	var b strings.Builder
	b.WriteString("# written by the lab\n")
	for _, d := range out {
		b.WriteString(d + "\n")
	}
	return []byte(b.String()), nil
}

// The decision store file, schema 1 (internal/lib/checker/store/API.md, lab/PLAN.md L-D6): the lab
// seeds and reads it by its documented format instead of importing the checker's child.
type storeFile struct {
	Schema   int                      `json:"schema"`
	Networks map[string]*storeNetwork `json:"networks"`
}

type storeNetwork struct {
	Label     string                   `json:"label,omitempty"`
	LastSeen  time.Time                `json:"last_seen"`
	Decisions map[string]storeDecision `json:"decisions"`
}

type storeDecision struct {
	Domain    string                 `json:"domain"`
	Route     route.Route            `json:"route"`
	Source    route.Source           `json:"source"`
	DecidedAt time.Time              `json:"decided_at"`
	CheckedAt time.Time              `json:"checked_at"`
	ExpiresAt time.Time              `json:"expires_at"`
	Pending   *storePending          `json:"pending,omitempty"`
	Last      map[route.Route]string `json:"last,omitempty"`
}

type storePending struct {
	Route route.Route `json:"route"`
	Count int         `json:"count"`
	Since time.Time   `json:"since"`
}

// seedTTL is the decide.Policy TTL default: a seeded decision is fresh, not expired.
const seedTTL = 24 * time.Hour

// seedDecisions renders decisions.json with the same decisions under every network ID in ids.
// Routes must be checkable (direct, desync, tunnel): the checker never stores proxy or block.
func seedDecisions(ids []string, ds map[string]route.Route, now time.Time) ([]byte, error) {
	return seedDecisionSets([]decisionSet{{IDs: ids, Decisions: ds}}, now)
}

// decisionSet is one network's seeded decisions, stored under every ID in IDs.
type decisionSet struct {
	IDs       []string
	Decisions map[string]route.Route
}

// seedDecisionSets renders decisions.json with one entry per ID of every set (several networks, for
// scenarios that switch the ISP). A network ID may appear in one set only.
func seedDecisionSets(sets []decisionSet, now time.Time) ([]byte, error) {
	f := storeFile{Schema: 1, Networks: map[string]*storeNetwork{}}
	for _, set := range sets {
		if len(set.IDs) == 0 {
			return nil, errors.New("lab: seed decisions: no network id")
		}
		decisions := map[string]storeDecision{}
		for d, r := range set.Decisions {
			n, err := route.Normalize(d)
			if err != nil {
				return nil, fmt.Errorf("lab: seed decisions: %w", err)
			}
			if !r.Checkable() {
				return nil, fmt.Errorf("lab: seed decisions: %s: route %q is not decidable", n, r)
			}
			decisions[n] = storeDecision{
				Domain: n, Route: r, Source: route.SourceChecked,
				DecidedAt: now.UTC(), CheckedAt: now.UTC(), ExpiresAt: now.Add(seedTTL).UTC(),
			}
		}
		for _, id := range set.IDs {
			if _, dup := f.Networks[id]; dup {
				return nil, fmt.Errorf("lab: seed decisions: network id %q in two sets", id)
			}
			cp := make(map[string]storeDecision, len(decisions))
			for k, v := range decisions {
				cp[k] = v
			}
			f.Networks[id] = &storeNetwork{LastSeen: now.UTC(), Decisions: cp}
		}
	}
	return json.MarshalIndent(f, "", " ")
}

// parseStore decodes decisions.json.
func parseStore(b []byte) (storeFile, error) {
	var f storeFile
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("lab: decisions.json: %w", err)
	}
	if f.Schema != 1 {
		return f, fmt.Errorf("lab: decisions.json: schema %d, want 1", f.Schema)
	}
	return f, nil
}

// gatewayNetworkID is the network ID netwatch derives without an ASN (checker PLAN M6, netwatch
// identify.go): "gw:" + hex(sha256(salt|gatewayMAC|SSID))[:16]. In the lab the SSID is empty.
func gatewayNetworkID(salt string, mac net.HardwareAddr, ssid string) string {
	sum := sha256.Sum256([]byte(salt + "|" + mac.String() + "|" + ssid))
	return "gw:" + hex.EncodeToString(sum[:])[:16]
}

// networkIDs lists every ID the checker may give an ISP's network, the ASN form (the contract)
// first: "asn:<n>" (or "asn:<n>/gw:<hash>" with includegateway), then the gateway fallback.
func networkIDs(asn uint32, gwID string, includeGateway bool) []string {
	a := "asn:" + strconv.FormatUint(uint64(asn), 10)
	if includeGateway {
		a += "/" + gwID
	}
	return []string{a, gwID}
}
