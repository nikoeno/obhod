package dialer

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
	"github.com/nikoejento/obhod/internal/lib/route"
)

func TestFakeScript(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	boom := errors.New("boom")
	f := NewFake(route.Direct, Step{Err: boom}, Step{Addr: "1.2.3.4:443", Conn: a}, Step{Addr: "x:1"})
	if f.Route() != route.Direct {
		t.Fatal("route")
	}
	ctx := context.Background()
	if _, err := f.DialContext(ctx, "tcp", "1.2.3.4:443"); err != boom {
		t.Fatalf("step 1: %v", err)
	}
	if c, err := f.DialContext(ctx, "tcp", "1.2.3.4:443"); err != nil || c != a {
		t.Fatalf("step 2: %v", err)
	}
	var le *verdict.LocalErr
	if _, err := f.DialContext(ctx, "tcp", "other:1"); !errors.As(err, &le) {
		t.Fatalf("address mismatch must be local: %v", err)
	}
	if _, err := f.DialContext(ctx, "tcp", "x:1"); !errors.As(err, &le) {
		t.Fatalf("exhausted script must be local: %v", err)
	}
}

func TestDirectDialsLocalListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
		}
	}()
	d, err := NewDirect(DirectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Route() != route.Direct {
		t.Fatal("route")
	}
	c, err := d.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}
