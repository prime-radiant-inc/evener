package hub

import (
	"net"
	"net/netip"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

var mobileCGNATPrefix = netip.MustParsePrefix("100.64.0.0/10")

// mobilePairingOrigin prefers the operator's configured default and the
// browser's usable origin. A local browser can instead use a network address
// on which this Hub actually listens.
func mobilePairingOrigin(cfg hubcore.WebConfig, proposed string) (string, bool) {
	if cfg.MobileBaseURL != "" {
		return safeMobileOrigin(cfg.MobileBaseURL)
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
		rank := mobileAddressRank(address)
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

// Tailscale's IPv4 address works across the user's networks. Other IPv4
// private addresses precede IPv6 so the default also serves IPv4-only clients.
// Public addresses provide a fallback when no private network is available.
func mobileAddressRank(address netip.Addr) int {
	if mobileCGNATPrefix.Contains(address) {
		return 0
	}
	if address.Is4() && address.IsPrivate() {
		return 1
	}
	if address.IsPrivate() {
		return 2
	}
	if address.Is4() {
		return 3
	}
	return 4
}
