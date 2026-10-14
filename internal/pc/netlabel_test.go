package pc

import (
	"testing"

	"github.com/nikoejento/obhod/internal/lib/checker"
)

func TestNetworkLabel(t *testing.T) {
	for _, c := range []struct {
		n    checker.Network
		want string
	}{
		{checker.Network{ASN: 64500, ASName: "ispa"}, "ispa"},
		{checker.Network{ASN: 64500}, "AS64500"},
		{checker.Network{}, "unknown network"},
		{checker.Network{ID: "gw:abc"}, "unknown network"},
	} {
		if got := networkLabel(c.n); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.n, got, c.want)
		}
	}
}
