package pc

import (
	"strings"
	"testing"

	"github.com/nikoejento/obhod/internal/lib/route"
)

// A table published while Connect runs (sing-box is starting) must still
// trigger a reload: the reload loop only starts after Connect.
func TestTablePublishedDuringConnectTriggersReload(t *testing.T) {
	h := newHarness(t, tbl(t, ent("a.example", route.Tunnel)))
	next := tbl(t, ent("a.example", route.Tunnel), ent("b.example", route.Tunnel))
	h.procs.onStart = func(name string) {
		if name == nameSingBox {
			h.chk.publish(next)
		}
	}
	h.start()
	h.waitOp("status.start")

	waitUntil(t, "debounce timer", func() bool { return h.fc.Waiters() >= 1 })
	h.fc.Advance(reloadDebounce)
	// The sing-box restart is held back by the minimum gap; the table is already applied.
	waitUntil(t, "table applied", func() bool { return len(h.app.currentTable().Entries) == 2 })
	h.fc.Advance(singBoxMinGap)
	h.waitOp("restart sing-box")
	if !strings.Contains(string(h.singBoxFile()), "b.example") {
		t.Fatalf("sing-box config lacks the late table:\n%s", h.singBoxFile())
	}
}

// In UI mode (disconnected) a published table still reaches the reload loop
// and updates the table the next Connect starts from.
func TestTablePublishedInUIModeIsApplied(t *testing.T) {
	h := newHarness(t, tbl(t, ent("a.example", route.Tunnel)), withUI(true))
	h.start()
	h.waitOp("status.start")
	h.chk.publish(tbl(t, ent("a.example", route.Tunnel), ent("b.example", route.Direct)))
	waitUntil(t, "debounce timer", func() bool { return h.fc.Waiters() >= 1 })
	h.fc.Advance(reloadDebounce)
	waitUntil(t, "table applied", func() bool { return len(h.app.currentTable().Entries) == 2 })
}
