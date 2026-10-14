package lab

import (
	"slices"
	"testing"
)

func TestParseClashConnections(t *testing.T) {
	body := `{"downloadTotal":1,"uploadTotal":2,"connections":[
	 {"id":"a","metadata":{"network":"tcp","destinationIP":"198.51.100.10","destinationPort":"443","host":"direct-a.test"},"upload":5,"download":6,"chains":["direct"]},
	 {"id":"b","metadata":{"network":"udp","destinationIP":"::ffff:198.51.100.11","destinationPort":"53","host":""},"chains":["tunnel","extra"]},
	 {"id":"c","metadata":{},"chains":null}]}`
	got, err := parseClashConnections([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d connections", len(got))
	}
	if got[0].Host != "direct-a.test" || got[0].Outbound() != "direct" || got[0].DstIP.String() != "198.51.100.10" || got[0].Download != 6 {
		t.Errorf("first: %+v", got[0])
	}
	if got[1].Outbound() != "extra" || !slices.Equal(got[1].Chains, []string{"tunnel", "extra"}) || got[1].DstIP.String() != "198.51.100.11" {
		t.Errorf("second (last chain element, unmapped IPv4): %+v", got[1])
	}
	if got[2].Outbound() != "" {
		t.Errorf("third: %+v", got[2])
	}
	if empty, err := parseClashConnections([]byte(`{"connections":null}`)); err != nil || len(empty) != 0 {
		t.Errorf("null list: %v %v", empty, err)
	}
	if _, err := parseClashConnections([]byte(`nope`)); err == nil {
		t.Error("accepted invalid JSON")
	}
}

func TestParseClashAPI(t *testing.T) {
	addr, secret, err := parseClashAPI([]byte(`{"experimental":{"clash_api":{"external_controller":"127.0.0.1:9090","secret":"s3"}}}`))
	if err != nil || addr != "127.0.0.1:9090" || secret != "s3" {
		t.Errorf("got %q %q %v", addr, secret, err)
	}
	for _, bad := range []string{`{}`, `{"experimental":{"clash_api":{"secret":"x"}}}`, `x`} {
		if _, _, err := parseClashAPI([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
