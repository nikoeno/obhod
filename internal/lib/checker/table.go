package checker

import (
	"github.com/nikoejento/obhod/internal/lib/checker/lib/model"
	"github.com/nikoejento/obhod/internal/lib/route"
)

func modelID(id string) model.NetworkID { return model.NetworkID(id) }

// rebuild computes the table for the current network from the lists and the store, and publishes it if it
// changed: static entries, then a stored decision per candidate, or UnknownDefault (source default) for
// candidates without one. On an unknown network every candidate gets the default.
func (c *Checker) rebuild() {
	c.mu.Lock()
	net := c.cur
	c.mu.Unlock()

	stored := map[string]model.Decision{}
	if net.Known {
		for _, d := range c.store.All(modelID(net.ID)) {
			stored[d.Domain] = d
		}
	}
	entries := make([]route.Entry, 0, len(c.static)+len(c.candidates))
	entries = append(entries, c.static...)
	for _, dom := range c.candidates {
		if d, ok := stored[dom]; ok {
			entries = append(entries, route.Entry{Domain: dom, Route: d.Route, Source: route.SourceChecked})
			continue
		}
		entries = append(entries, route.Entry{Domain: dom, Route: c.cfg.UnknownDefault, Source: route.SourceDefault})
	}
	id := ""
	if net.Known {
		id = net.ID
	}
	t, err := route.NewTable(id, c.clk.Now(), entries)
	if err != nil {
		c.warn("table not rebuilt: %v", err)
		return
	}
	c.publish(t)
}
