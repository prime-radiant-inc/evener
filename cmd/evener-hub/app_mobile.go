package hub

import (
	"context"
	"net/netip"
	"net/url"
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
		// Browsers parse a numeric final label as IPv4, even when the input
		// is malformed. Only canonical IPs accepted above can be shared with
		// the browser; do not publish a numeric spelling as a DNS hostname.
		if hasNumericHostSuffix(host) {
			return "", false
		}
		if u.Scheme == "http" && !strings.HasSuffix(hostLower, ".local") {
			return "", false
		}
	}
	return strings.TrimRight(raw, "/"), true
}

// hasNumericHostSuffix implements the WHATWG ends-in-a-number check on an
// ASCII hostname whose trailing dots have already been removed.
func hasNumericHostSuffix(host string) bool {
	last := host[strings.LastIndexByte(host, '.')+1:]
	if last == "" {
		return false
	}
	if strings.Trim(last, "0123456789") == "" {
		return true
	}
	return len(last) >= 2 && last[0] == '0' && (last[1] == 'x' || last[1] == 'X') && strings.Trim(last[2:], "0123456789abcdefABCDEF") == ""
}

func isPrivateMobileHTTPAddr(addr netip.Addr) bool {
	if addr.IsPrivate() || addr.IsLinkLocalUnicast() {
		return true
	}
	return addr.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(addr)
}
