package verdict

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestDefaultBlocking(t *testing.T) {
	for v, want := range defaultBlocking {
		if !v.Valid() || v.DefaultBlocking() != want {
			t.Errorf("%s: valid=%v blocking=%s want %s", v, v.Valid(), v.DefaultBlocking(), want)
		}
	}
	if Verdict("bogus").Valid() || Verdict("bogus").DefaultBlocking() != BlockingUnknown {
		t.Error("bogus verdict")
	}
	if len(defaultBlocking) != 19 {
		t.Errorf("verdict enum is frozen at 19 values, got %d", len(defaultBlocking))
	}
}

func TestLocal(t *testing.T) {
	base := errors.New("no uplink")
	err := Local(base)
	var le *LocalErr
	if !errors.As(err, &le) || !errors.Is(err, base) {
		t.Fatal("unwrap")
	}
	if Local(nil) != nil {
		t.Fatal("nil")
	}
}

func TestEvidenceJSON(t *testing.T) {
	in := Evidence{Err: "x", RxBytes: 5, RTT: time.Second, StageTimes: map[Stage]time.Duration{StageTCP: time.Millisecond},
		SNISwap: &SNISwapResult{SNI: "a.example", Verdict: OK}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Evidence
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Err != in.Err || out.RTT != in.RTT || out.StageTimes[StageTCP] != time.Millisecond || out.SNISwap.Verdict != OK {
		t.Fatalf("round trip: %+v", out)
	}
}
