package lab

// This file has no build tag on purpose: the Clash API wire format and the sing-box config reader
// are pure Go, so their unit test runs on any OS without root.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
)

// ClashConn is one live connection of sing-box's Clash API (GET /connections), the fields the
// scenarios assert on. The lab may not import internal/pc/lib/clashapi, so the wire shape is
// repeated here.
type ClashConn struct {
	ID       string
	Network  string // tcp | udp
	DstIP    netip.Addr
	DstPort  string
	Host     string   // the sniffed (or FakeIP-mapped) name
	Chains   []string // the last element is the outbound tag = the route name
	Upload   int64
	Download int64
}

// Outbound is the last element of the chain ("" for an empty chain).
func (c ClashConn) Outbound() string {
	if len(c.Chains) == 0 {
		return ""
	}
	return c.Chains[len(c.Chains)-1]
}

// parseClashConnections decodes the body of GET /connections.
func parseClashConnections(b []byte) ([]ClashConn, error) {
	var w struct {
		Connections []struct {
			ID       string   `json:"id"`
			Upload   int64    `json:"upload"`
			Download int64    `json:"download"`
			Chains   []string `json:"chains"`
			Metadata struct {
				Network         string `json:"network"`
				DestinationIP   string `json:"destinationIP"`
				DestinationPort string `json:"destinationPort"`
				Host            string `json:"host"`
			} `json:"metadata"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return nil, fmt.Errorf("lab: clash /connections: %w", err)
	}
	out := make([]ClashConn, 0, len(w.Connections))
	for _, c := range w.Connections {
		ip, _ := netip.ParseAddr(c.Metadata.DestinationIP)
		out = append(out, ClashConn{
			ID: c.ID, Network: c.Metadata.Network, DstIP: ip.Unmap(), DstPort: c.Metadata.DestinationPort,
			Host: c.Metadata.Host, Chains: c.Chains, Upload: c.Upload, Download: c.Download,
		})
	}
	return out, nil
}

// parseClashAPI reads the Clash API address and secret from a generated sing-box config
// (experimental.clash_api.external_controller and .secret).
func parseClashAPI(cfg []byte) (addr, secret string, err error) {
	var c struct {
		Experimental struct {
			ClashAPI struct {
				Controller string `json:"external_controller"`
				Secret     string `json:"secret"`
			} `json:"clash_api"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(cfg, &c); err != nil {
		return "", "", fmt.Errorf("lab: sing-box config: %w", err)
	}
	a := c.Experimental.ClashAPI
	if a.Controller == "" || a.Secret == "" {
		return "", "", errors.New("lab: sing-box config has no clash_api controller or secret")
	}
	return a.Controller, a.Secret, nil
}
