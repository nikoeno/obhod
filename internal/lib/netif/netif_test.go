package netif

import "testing"

func TestIsVirtualName(t *testing.T) {
	for n, want := range map[string]bool{"Wintun": true, "tun0": true, "wg0": true, "sing-tun": true, "eth0": false, "Ethernet": false, "Wi-Fi": false, "wlan0": false} {
		if isVirtualName(n) != want {
			t.Errorf("%s: want %v", n, want)
		}
	}
}
