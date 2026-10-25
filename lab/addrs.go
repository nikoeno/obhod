package lab

// Address plan of one lab (no build tag: the VPS config test uses it on any OS).
//
// Namespaces: obl<4 hex>-cli, obl<4 hex>-<isp> (one per profile), obl<4 hex>-inet, obl<4 hex>-vps.
//
//	cli  eth<i>  192.168.<10(i+1)>.2/24  ── lan  192.168.<10(i+1)>.1/24  ISP i (i = 0, 1, ...; at most 8)
//	                                         the ISP's plain DNS resolver listens on .1:53
//	ISP i  wan  203.0.113.<4i+1>/30      ── isp<i>  203.0.113.<4i+2>/30  inet
//	                                         .4i+1 is the ISP's public (SNAT) address = ClientAddr
//	vps  eth0   203.0.113.250/30         ── vps  203.0.113.249/30  inet;  .250 = VPSAddr
//	inet lo     198.51.100.2/32  fake DoH (HTTPS :443, test CA cert with an IP SAN)
//	            198.51.100.3/32  fakeasn echo (HTTP :80)
//	            198.51.100.4/32  plain DNS "public resolver" (:53, zone only), used by the VPS
//	            198.51.100.10 .. 198.51.100.249 /32  one per site (:443), in AddSite order;
//	            the first one is reality.test, the VPS's Reality handshake target
//
// Routes: cli default via the active ISP's .1 (the other client links are down); each ISP default
// via its inet side; vps default via 203.0.113.249; inet needs none (every ISP public address and
// the VPS are on connected /30s, and client addresses never reach inet because of the SNAT).
// Forwarding is on in the ISPs and inet only. Site addresses live on inet's loopback, so no extra
// links are needed.
//
// Ranges: client links stay in 192.168/16 (excluded from the app's TUN, like a home LAN); what the
// client reaches through the TUN is in 198.51.100/24 and 203.0.113/24; nothing uses 10/8,
// 169.254/16 or 198.18.0.0/15.
const (
	labPrefix    = "obl"           // + 4 hex per Up
	maxISPs      = 8               // eth0..eth7, 192.168.10-80.0/24, 203.0.113.0-31
	dohAddr      = "198.51.100.2"  // fake DoH
	echoAddr     = "198.51.100.3"  // fakeasn echo
	resolverAddr = "198.51.100.4"  // plain DNS for the VPS
	siteFirst    = 10              // 198.51.100.10 is the first site
	siteLast     = 249             // last site address
	sitePrefix   = "198.51.100."   // sites, DoH, echo, resolver
	vpsInetAddr  = "203.0.113.249" // inet side of the VPS link
	vpsAddr      = "203.0.113.250" // VPSAddr
	vpsLinkBits  = 30              // /30 for every ISP↔inet and the VPS↔inet link
	vlessPort    = 443             // VLESS+Reality inbound on the VPS
	socksPort    = 1080            // SOCKS5 inbound on the VPS
	realityHost  = "reality.test"  // lab site used as the Reality handshake target
	queueBase    = 10              // NFQUEUE number of ISP i is queueBase+i
)
