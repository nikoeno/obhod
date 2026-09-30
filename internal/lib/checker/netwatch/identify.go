package netwatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"strconv"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/checker/lib/model"
	"github.com/nikoejento/obhod/internal/lib/netif"
)

// uplinkKey identifies the physical attachment: interface, gateway address and gateway MAC.
func uplinkKey(ifc netif.Interface) string {
	gw := ""
	if ifc.Gateway.IsValid() {
		gw = ifc.Gateway.String()
	}
	return ifc.Name + "|" + gw + "|" + ifc.GatewayMAC.String()
}

// gatewayID hashes the gateway MAC and SSID with the per-install salt. It returns "" when neither is known
// (a bare gateway IP such as 192.168.1.1 says nothing about which network this is).
func gatewayID(salt string, ifc netif.Interface) string {
	if len(ifc.GatewayMAC) == 0 && ifc.SSID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(salt + "|" + ifc.GatewayMAC.String() + "|" + ifc.SSID))
	return hex.EncodeToString(sum[:])[:16]
}

// identify determines the current network and publishes it.
//
//  1. Uplink → public IP (direct) → ASN gives "asn:<n>"; with IncludeGateway, "asn:<n>/gw:<hash>".
//  2. Without an ASN, a gateway hash gives "gw:<hash>".
//  3. When the lookup fails but the uplink is the one the last identification used, the last ID is kept
//     (a flaky echo service must not flip the network); otherwise the network is unknown.
func (w *Watcher) identify(ctx context.Context) {
	ifc, err := w.deps.Uplink(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.publish(Network{}, "")
		}
		return
	}
	key := uplinkKey(ifc)

	var (
		id   model.NetworkID
		asn  analyzer.ASN
		ip   = w.currentIP(key)
		asOK bool
	)
	if pub, perr := w.deps.PublicIP(ctx); perr == nil && pub.IsValid() && w.deps.Resolver != nil {
		if a, aerr := analyzer.LookupASN(ctx, w.deps.Resolver, pub); aerr == nil && a.Number != 0 {
			asn, asOK, ip = a, true, pub
		}
	}
	gw := gatewayID(w.cfg.Salt, ifc)
	switch {
	case asOK && w.cfg.IncludeGateway && gw != "":
		id = model.NetworkID("asn:" + strconv.FormatUint(uint64(asn.Number), 10) + "/gw:" + gw)
	case asOK:
		id = model.NetworkID("asn:" + strconv.FormatUint(uint64(asn.Number), 10))
	default:
		w.mu.Lock()
		prev, prevKey := w.cur, w.curKey
		w.mu.Unlock()
		if prev.Known && prevKey == key {
			prev.Uplink = ifc.Name
			w.publish(prev, key)
			return
		}
		if gw != "" {
			id = model.NetworkID("gw:" + gw)
		}
	}
	if !id.Valid() {
		if ctx.Err() == nil {
			w.publish(Network{Uplink: ifc.Name}, key)
		}
		return
	}
	w.publish(Network{ID: id, ASN: asn.Number, ASName: asn.Name, PublicIP: ip, Uplink: ifc.Name, Known: true}, key)
}

// currentIP returns the last known public IP if it belongs to the same uplink.
func (w *Watcher) currentIP(key string) (ip netip.Addr) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.curKey == key {
		return w.cur.PublicIP
	}
	return ip
}
