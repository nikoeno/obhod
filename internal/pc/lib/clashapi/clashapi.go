package clashapi

import (
	"context"
	"net/http"
	"time"
)

// Connection is one connection tracked by sing-box.
type Connection struct {
	ID       string
	Host     string // metadata.host (sniffed SNI/Host)
	DstIP    string
	DstPort  string
	Network  string // tcp | udp
	Process  string // metadata.processPath (if available)
	Upload   int64
	Download int64
	Start    time.Time
	Chains   []string // outbound chain, last = outbound tag
	Rule     string
}

// Snapshot is the state of GET /connections at one moment.
type Snapshot struct {
	At            time.Time
	UploadTotal   int64
	DownloadTotal int64
	Connections   []Connection
}

// Client reads the Clash-compatible API of sing-box.
type Client interface {
	Connections(ctx context.Context) (Snapshot, error)
	Version(ctx context.Context) (string, error)
}

// New returns a Client for baseURL (for example "http://127.0.0.1:9090").
// The secret is sent as a Bearer token only when non-empty. A nil hc selects a
// client with a short timeout that never uses a proxy, so a system proxy cannot
// capture loopback traffic.
func New(baseURL, secret string, hc *http.Client) Client {
	if hc == nil {
		hc = &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{Proxy: nil},
		}
	}
	return &client{base: baseURL, secret: secret, hc: hc, now: time.Now}
}
