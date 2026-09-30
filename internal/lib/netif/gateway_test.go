package netif

import (
	"net/netip"
	"os"
	"strings"
	"testing"
)

func TestParseRouteTable(t *testing.T) {
	f, err := os.Open("testdata/route.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	routes := parseRouteTable(f)
	if len(routes) != 3 {
		t.Fatalf("want 3 default routes (eth0, wlan0, tun0), got %+v", routes)
	}
	r, ok := pickDefaultRoute(routes, "eth0")
	if !ok || r.Gateway != netip.MustParseAddr("192.168.0.1") || r.Metric != 100 {
		t.Errorf("eth0: %+v", r)
	}
	r, _ = pickDefaultRoute(routes, "wlan0")
	if r.Gateway != netip.MustParseAddr("172.16.0.1") {
		t.Errorf("wlan0: %+v", r)
	}
	if r, _ = pickDefaultRoute(routes, ""); r.Iface != "tun0" {
		t.Errorf("lowest metric overall: %+v", r)
	}
	if _, ok := pickDefaultRoute(routes, "eth9"); ok {
		t.Error("unknown iface must not match")
	}
	if _, ok := pickDefaultRoute(nil, ""); ok {
		t.Error("empty table must not match")
	}
}

func TestParseRouteTableMalformed(t *testing.T) {
	if got := parseRouteTable(strings.NewReader("")); len(got) != 0 {
		t.Errorf("empty: %+v", got)
	}
	if got := parseRouteTable(strings.NewReader("eth0 00000000 XX 0003 0 0 1 00000000\n\x00\x01\n")); len(got) != 0 {
		t.Errorf("malformed: %+v", got)
	}
}

func TestParseARPTable(t *testing.T) {
	data, err := os.ReadFile("testdata/arp.txt")
	if err != nil {
		t.Fatal(err)
	}
	arp := string(data)
	gw := netip.MustParseAddr
	if m := parseARPTable(strings.NewReader(arp), gw("192.168.0.1"), "eth0"); m.String() != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("eth0: %v", m)
	}
	if m := parseARPTable(strings.NewReader(arp), gw("172.16.0.1"), "wlan0"); m.String() != "11:22:33:44:55:66" {
		t.Errorf("wlan0 must skip the incomplete entry: %v", m)
	}
	if m := parseARPTable(strings.NewReader(arp), gw("192.168.0.1"), "wlan0"); m != nil {
		t.Errorf("wrong device: %v", m)
	}
	if m := parseARPTable(strings.NewReader(arp), gw("10.0.0.9"), "eth0"); m != nil {
		t.Errorf("bad mac: %v", m)
	}
	if m := parseARPTable(strings.NewReader(arp), gw("1.2.3.4"), ""); m != nil {
		t.Errorf("absent: %v", m)
	}
}
