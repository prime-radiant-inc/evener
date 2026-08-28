package hub

import (
	"context"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/idna"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/internal/appserver"
)

var mobileHostnameProfile = idna.New(idna.MapForLookup(), idna.StrictDomainName(false), idna.CheckHyphens(false))

func registerMobilePairingHandler(server *appserver.Server, cfg hubcore.WebConfig) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMobilePairing, func(_ context.Context, params appwire.MobilePairingParams) (appwire.MobilePairingResponse, error) {
		return mobilePairing(cfg, params)
	})
}

func mobilePairing(cfg hubcore.WebConfig, params appwire.MobilePairingParams) (appwire.MobilePairingResponse, error) {
	origin := params.Origin
	if cfg.MobileBaseURL != "" {
		origin = cfg.MobileBaseURL
	}
	base, ok := safeMobileOrigin(origin)
	if !ok {
		return appwire.MobilePairingResponse{}, appwire.Conflict("mobile pairing requires a reachable non-loopback Hub origin")
	}
	return appwire.MobilePairingResponse{AuthURL: hubedge.AuthURLFor(base, cfg.AuthToken)}, nil
}

// safeMobileOrigin applies the connection policy the mobile app enforces before
// putting an origin in a QR code. HTTP may name only a private-network address
// (or a .local name which the app resolves and validates); HTTPS may name a
// public or private host. Neither may name loopback, which refers to the phone
// after scanning rather than the Hub.
func safeMobileOrigin(raw string) (string, bool) {
	if err := validateMobileBaseURL(raw); err != nil {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	host := strings.TrimRight(u.Hostname(), ".")
	if _, err := netip.ParseAddr(host); err != nil {
		// Browsers apply UTS #46 mappings before interpreting a hostname. Do
		// the same so Unicode spellings of numeric addresses cannot bypass the
		// IP and legacy-numeric checks below. Canonical IP literals bypass IDNA
		// because IPv6 colons are not valid domain-name runes.
		host, err = mobileHostnameProfile.ToASCII(host)
		if err != nil {
			return "", false
		}
		host = strings.TrimRight(host, ".")
	}
	// Reject localhost and its reserved subdomains in all case and
	// trailing-dot spellings.
	hostLower := strings.ToLower(host)
	if host == "" || hostLower == "localhost" || strings.HasSuffix(hostLower, ".localhost") {
		return "", false
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if addr.IsLoopback() {
			return "", false
		}
		if u.Scheme == "http" && !isPrivateMobileHTTPAddr(addr) {
			return "", false
		}
	} else {
		// Some clients accept inet_aton-style numeric addresses such as 127.1,
		// 2130706433, or 0x7f000001. Reject those spellings deterministically
		// rather than resolving a host while serving the pairing request.
		if isLegacyIPv4Literal(host) {
			return "", false
		}
		if u.Scheme == "http" && !strings.HasSuffix(hostLower, ".local") {
			return "", false
		}
	}
	return strings.TrimRight(raw, "/"), true
}

func isLegacyIPv4Literal(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return false
	}
	for i, part := range parts {
		base := 10
		digits := part
		if len(part) > 2 && part[0] == '0' && (part[1] == 'x' || part[1] == 'X') {
			base = 16
			digits = part[2:]
		} else if len(part) > 1 && part[0] == '0' {
			base = 8
		}
		if digits == "" {
			return false
		}
		value, err := strconv.ParseUint(digits, base, 32)
		if err != nil {
			return false
		}
		bits := 8
		if i == len(parts)-1 {
			bits = 8 * (5 - len(parts))
		}
		if value >= uint64(1)<<bits {
			return false
		}
	}
	return true
}

func isPrivateMobileHTTPAddr(addr netip.Addr) bool {
	if addr.IsPrivate() || addr.IsLinkLocalUnicast() {
		return true
	}
	return addr.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(addr)
}
