package hub

import (
	"net"
	"net/netip"
	"slices"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

var mobileCGNATPrefix = netip.MustParsePrefix("100.64.0.0/10")

// mobilePairingOrigin prefers the operator's configured default and the
// browser's usable origin. A local browser can instead use a network address
// on which this Hub actually listens.
func mobilePairingOrigin(cfg hubcore.WebConfig, proposed string) (string, bool) {
	if cfg.PublicURL != "" {
		return safeMobileOrigin(cfg.PublicURL)
	}
	if origin, ok := safeMobileOrigin(proposed); ok {
		return origin, true
	}
	host, port, err := net.SplitHostPort(cfg.HubAddr)
	if err != nil {
		return "", false
	}
	bound, err := netip.ParseAddr(host)
	if host != "" && err != nil {
		return "", false
	}
	if bound.IsValid() && !bound.IsUnspecified() {
		return safeMobileOrigin("http://" + net.JoinHostPort(bound.String(), port))
	}
	interfaceAddrs := cfg.MobileInterfaceAddrs
	if interfaceAddrs == nil {
		interfaceAddrs = net.InterfaceAddrs
	}
	addresses, err := interfaceAddrs()
	if err != nil {
		return "", false
	}
	routeAddrs := cfg.MobileRouteAddrs
	if routeAddrs == nil {
		routeAddrs = mobileRouteAddrs
	}
	routes := routeAddrs()
	var selected netip.Addr
	selectedRank := 0
	for _, network := range addresses {
		prefix, err := netip.ParsePrefix(network.String())
		if err != nil {
			continue
		}
		address := prefix.Addr().Unmap()
		if !address.IsGlobalUnicast() || (bound.Is4() && !address.Is4()) {
			continue
		}
		rank := mobileAddressRank(address, routes)
		if !selected.IsValid() || rank < selectedRank || (rank == selectedRank && address.Less(selected)) {
			selected = address
			selectedRank = rank
		}
	}
	if !selected.IsValid() {
		return "", false
	}
	return safeMobileOrigin("http://" + net.JoinHostPort(selected.String(), port))
}

// Tailscale's IPv4 address works across the user's networks. The OS's outbound
// routes identify network-facing addresses ahead of container bridges. Other
// private addresses and finally public addresses provide an offline fallback.
func mobileAddressRank(address netip.Addr, routes []netip.Addr) int {
	if mobileCGNATPrefix.Contains(address) {
		return 0
	}
	if slices.Contains(routes, address) {
		if address.Is4() {
			return 1
		}
		return 2
	}
	if address.Is4() && address.IsPrivate() {
		return 3
	}
	if address.IsPrivate() {
		return 4
	}
	if address.Is4() {
		return 5
	}
	return 6
}

// Connecting a UDP socket asks the kernel which source address it would use.
// No packets are sent: the sockets are closed without writing, and the literal
// documentation addresses require no DNS lookup or reachable remote service.
func mobileRouteAddrs() []netip.Addr {
	var addresses []netip.Addr
	for _, host := range []string{"192.0.2.1", "2001:db8::1"} {
		conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP(host), Port: 9})
		if err != nil {
			continue
		}
		address := conn.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
		_ = conn.Close()
		addresses = append(addresses, address)
	}
	return addresses
}
