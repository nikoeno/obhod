package netwatch

// First-run regression tests: on a real Windows machine the network was never identified (no gateway MAC,
// no SSID, no echo endpoints), so no site was ever probed. Identification here makes no network calls
// (no echo endpoint is configured and nothing is dialed), so these tests need neither a network nor a tag.

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/netif"
)

// identifyOnce runs one identification with the given uplink and nothing else (no echo, no resolver).
func identifyOnce(t *testing.T, up func(context.Context) (netif.Interface, error)) Network {
	t.Helper()
	w := New(Config{Salt: "test-salt"}, Deps{
		Clock:  clock.NewFake(t0),
		Uplink: up,
		Events: func(context.Context) (<-chan netif.Event, error) { return nil, errors.New("no events") },
	})
	w.identify(context.Background())
	return w.Current()
}

// For each set of facts an OS can give offline, the network is identified, with the ID kind that fits.
func TestOfflineFactsIdentifyTheNetwork(t *testing.T) {
	gw := netip.MustParseAddr("192.168.0.1")
	mac := net.HardwareAddr{2, 0, 0, 0, 0, 1}
	cases := []struct {
		name   string
		ifc    netif.Interface
		known  bool
		prefix string
	}{
		{"Windows: adapter and gateway IP, no MAC, no SSID", netif.Interface{Name: "Ethernet", Index: 7, Gateway: gw}, true, "local:"},
		{"Windows Wi-Fi adapter name with a space", netif.Interface{Name: "Wi-Fi 2", Index: 9, Gateway: gw}, true, "local:"},
		{"Linux: gateway and gateway MAC", netif.Interface{Name: "eth0", Index: 2, Gateway: gw, GatewayMAC: mac}, true, "gw:"},
		{"Wi-Fi: SSID, no MAC", netif.Interface{Name: "wlan0", Index: 3, Gateway: gw, SSID: "Home"}, true, "gw:"},
		{"Wi-Fi: SSID and MAC", netif.Interface{Name: "wlan0", Index: 3, Gateway: gw, GatewayMAC: mac, SSID: "Home"}, true, "gw:"},
		{"MAC without a gateway address", netif.Interface{Name: "eth0", GatewayMAC: mac}, true, "gw:"},
		{"SSID without a gateway address", netif.Interface{Name: "wlan0", SSID: "Cafe"}, true, "gw:"},
		// Truly nothing to go on: unknown, never a made-up ID.
		{"adapter only", netif.Interface{Name: "Ethernet", Index: 7}, false, ""},
		{"gateway only, no adapter name", netif.Interface{Gateway: gw}, false, ""},
		{"empty interface", netif.Interface{}, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := identifyOnce(t, func(context.Context) (netif.Interface, error) { return c.ifc, nil })
			if n.Known != c.known {
				t.Fatalf("Known = %v (ID %q), want %v", n.Known, n.ID, c.known)
			}
			if !c.known {
				if n.ID != "" {
					t.Fatalf("unknown network has ID %q", n.ID)
				}
				return
			}
			if !strings.HasPrefix(string(n.ID), c.prefix) || !n.ID.Valid() {
				t.Fatalf("ID = %q, want prefix %q", n.ID, c.prefix)
			}
			if strings.Contains(string(n.ID), gw.String()) || strings.Contains(string(n.ID), c.ifc.Name) {
				t.Fatalf("ID %q shows the gateway or the adapter name in clear", n.ID)
			}
		})
	}
	t.Run("no uplink at all", func(t *testing.T) {
		n := identifyOnce(t, func(context.Context) (netif.Interface, error) { return netif.Interface{}, netif.ErrNoUplink })
		if n.Known || n.ID != "" {
			t.Fatalf("network = %+v with no uplink", n)
		}
	})
}

// The real operating system of the machine that runs the tests: netif.Uplink and the real identify path.
// With an uplink and a default gateway the network must become Known, with a local: or gw: ID. Skipped
// when the machine has none (a CI container without a network).
func TestRealOSUplinkIsIdentified(t *testing.T) {
	ifc, err := netif.Uplink(context.Background())
	if err != nil {
		t.Skipf("no physical uplink on this machine: %v", err)
	}
	if !ifc.Gateway.IsValid() {
		t.Skipf("uplink %q has no default gateway on this machine: nothing to identify offline", ifc.Name)
	}
	w := New(Config{Salt: "test-salt"}, Deps{
		Clock:  clock.NewFake(t0),
		Events: func(context.Context) (<-chan netif.Event, error) { return nil, errors.New("no events") },
		// Uplink and PublicIP stay nil: the real netif.Uplink, and an echo lookup with no endpoints
		// (it fails at once, without a socket).
	})
	w.identify(context.Background())
	n := w.Current()
	if !n.Known {
		t.Fatalf("this machine has uplink %q with a gateway, but the network is not identified (no site would ever be probed)", ifc.Name)
	}
	if !strings.HasPrefix(string(n.ID), "local:") && !strings.HasPrefix(string(n.ID), "gw:") {
		t.Fatalf("ID = %q, want local: or gw:", n.ID)
	}
	if n.Uplink == "" || !n.ID.Valid() || n.PublicIP.IsValid() {
		t.Fatalf("network = %+v", n)
	}
}
