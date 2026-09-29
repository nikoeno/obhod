package analyzer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer/classify"
	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
)

// bulkTimeout is the RTT-adaptive no-progress timeout of the bulk test (DESIGN §4.1).
func bulkTimeout(total, rtt time.Duration) time.Duration {
	d := max(total/4, 6*rtt)
	return min(d, total)
}

// bulk runs the 16–20 KB test on the established, keep-alive connection: it moves o.Bulk.Size bytes
// and watches for a stall. Sizes are wire bytes: everything the DPI could count, TLS overhead included.
func (p *prober) bulk(ctx context.Context, a attemptResult, t Target, uc io.ReadWriter, br *bufio.Reader, conn *countConn, o Options, rtt time.Duration) attemptResult {
	t0 := p.clk.Now()
	overall := time.Now().Add(o.Timeouts.Bulk)
	step := bulkTimeout(o.Timeouts.Bulk, rtt)
	// deadline returns the next per-operation deadline, never past the overall one.
	deadline := func() time.Time {
		d := time.Now().Add(step)
		if d.After(overall) {
			return overall
		}
		return d
	}
	var err error
	if o.Bulk.Mode == "upload" {
		err = bulkUpload(uc, br, conn, t, o, deadline)
	} else {
		err = bulkDownload(uc, br, conn, t, o, deadline)
	}
	a.ev.StageTimes[verdict.StageBulk] = p.clk.Since(t0)
	a.ev.RxBytes, a.ev.TxBytes = conn.rx.Load(), conn.tx.Load()
	a.stage = verdict.StageBulk
	if err == nil {
		a.v = verdict.OK
		return a
	}
	if ctx.Err() != nil {
		err = verdict.Local(ctx.Err())
	}
	a.v = classify.Classify(classify.Input{
		Stage: verdict.StageBulk, Err: err, RxBytes: a.ev.RxBytes, TxBytes: a.ev.TxBytes,
		Elapsed: p.clk.Since(t0), Timeout: step, StallWin: [2]int64{o.Bulk.StallMin, o.Bulk.StallMax},
	})
	a.ev.Err = errChain(err)
	return a
}

func bulkDownload(w io.Writer, br *bufio.Reader, conn *countConn, t Target, o Options, deadline func() time.Time) error {
	path := t.BulkPath
	if path == "" {
		path = t.Path
	}
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: %s\r\nAccept: */*\r\nConnection: close\r\n\r\n", path, hostHeader(t.Domain, t.Port), userAgent)
	conn.SetWriteDeadline(deadline())
	if _, err := io.WriteString(w, req); err != nil {
		return err
	}
	conn.SetReadDeadline(deadline())
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	for got := 0; got < o.Bulk.Size; {
		conn.SetReadDeadline(deadline())
		n, err := resp.Body.Read(buf)
		got += n
		if errors.Is(err, io.EOF) {
			return nil // the object is smaller than the bulk size; nothing more to test
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func bulkUpload(w io.Writer, br *bufio.Reader, conn *countConn, t Target, o Options, deadline func() time.Time) error {
	path := t.BulkPath
	if path == "" {
		path = t.Path
	}
	req := fmt.Sprintf("POST %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: %s\r\nAccept: */*\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		path, hostHeader(t.Domain, t.Port), userAgent, o.Bulk.Size)
	conn.SetWriteDeadline(deadline())
	if _, err := io.WriteString(w, req); err != nil {
		return err
	}
	chunk := make([]byte, 4096)
	for i := range chunk {
		chunk[i] = byte('a' + i%26)
	}
	for sent := 0; sent < o.Bulk.Size; {
		n := min(len(chunk), o.Bulk.Size-sent)
		conn.SetWriteDeadline(deadline())
		if _, err := w.Write(chunk[:n]); err != nil {
			return err
		}
		sent += n
	}
	conn.SetReadDeadline(deadline())
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodPost})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
