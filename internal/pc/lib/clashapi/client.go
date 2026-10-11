package clashapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxBody bounds a response body; a busy sing-box can list thousands of connections.
const maxBody = 64 << 20

type client struct {
	base   string
	secret string
	hc     *http.Client
	now    func() time.Time
}

type wireSnapshot struct {
	UploadTotal   int64      `json:"uploadTotal"`
	DownloadTotal int64      `json:"downloadTotal"`
	Connections   []wireConn `json:"connections"`
}

type wireConn struct {
	ID       string       `json:"id"`
	Metadata wireMetadata `json:"metadata"`
	Upload   int64        `json:"upload"`
	Download int64        `json:"download"`
	Start    string       `json:"start"`
	Chains   []string     `json:"chains"`
	Rule     string       `json:"rule"`
}

type wireMetadata struct {
	Network         string `json:"network"`
	DestinationIP   string `json:"destinationIP"`
	DestinationPort string `json:"destinationPort"`
	Host            string `json:"host"`
	ProcessPath     string `json:"processPath"`
}

type wireVersion struct {
	Version string `json:"version"`
}

func (c *client) Connections(ctx context.Context) (Snapshot, error) {
	var w wireSnapshot
	if err := c.get(ctx, "/connections", &w); err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{
		At:            c.now(),
		UploadTotal:   w.UploadTotal,
		DownloadTotal: w.DownloadTotal,
		Connections:   make([]Connection, 0, len(w.Connections)),
	}
	for _, wc := range w.Connections {
		conn := Connection{
			ID:       wc.ID,
			Host:     wc.Metadata.Host,
			DstIP:    wc.Metadata.DestinationIP,
			DstPort:  wc.Metadata.DestinationPort,
			Network:  wc.Metadata.Network,
			Process:  wc.Metadata.ProcessPath,
			Upload:   wc.Upload,
			Download: wc.Download,
			Chains:   wc.Chains,
			Rule:     wc.Rule,
		}
		if wc.Start != "" {
			t, err := time.Parse(time.RFC3339Nano, wc.Start)
			if err != nil {
				return Snapshot{}, fmt.Errorf("clashapi: GET /connections: bad start %q: %w", wc.Start, err)
			}
			conn.Start = t
		}
		s.Connections = append(s.Connections, conn)
	}
	return s, nil
}

func (c *client) Version(ctx context.Context) (string, error) {
	var w wireVersion
	if err := c.get(ctx, "/version", &w); err != nil {
		return "", err
	}
	return w.Version, nil
}

// get performs GET path and decodes the JSON body into out. Errors name the
// endpoint and status only; they never contain the secret.
func (c *client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.base, "/")+path, nil)
	if err != nil {
		return fmt.Errorf("clashapi: GET %s: %s", path, c.scrub(err))
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("clashapi: GET %s: %w", path, ctx.Err())
		}
		return fmt.Errorf("clashapi: GET %s: %s", path, c.scrub(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("clashapi: GET %s: %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out); err != nil {
		return fmt.Errorf("clashapi: GET %s: decode: %w", path, err)
	}
	return nil
}

// scrub renders err without the secret, in case a transport error echoes it.
func (c *client) scrub(err error) string {
	s := err.Error()
	if c.secret != "" {
		s = strings.ReplaceAll(s, c.secret, "***")
	}
	return s
}
